package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

func testConfiguration(directory string) skill.Configuration {
	configuration := skill.DefaultConfiguration()
	configuration.Directory = directory
	return configuration
}

func writeSkill(t *testing.T, root, directory, document string, resources map[string][]byte) {
	t.Helper()
	skillRoot := filepath.Join(root, directory)
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, data := range resources {
		path := filepath.Join(skillRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func skillDocument(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\nmetadata:\n  neondoll.revision: \"3\"\n---\n\n" + body
}

func TestLocalCatalogDiscoversLoadsAndRediscoversAfterRestart(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "runtime-inspection", skillDocument("runtime-inspection", "Inspect runtime details when platform facts matter.", "# Instructions\nUse runtime.info."), map[string][]byte{
		"references/fields.md": []byte("Only report relevant fields."),
		"assets/icon.bin":      {0, 1, 2, 255},
	})
	configuration := testConfiguration(root)
	catalog, err := Open(configuration, Options{})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := catalog.Discover(context.Background(), skill.DiscoverRequest{Query: "platform", Surface: skill.SurfacePlan, Limit: 10})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	if summaries[0].Reference.ID != "runtime-inspection" || summaries[0].Reference.Revision != 3 || summaries[0].Source != "local" || summaries[0].Directory != "runtime-inspection" {
		t.Fatalf("summary = %+v", summaries[0])
	}
	loaded, err := catalog.Load(context.Background(), skill.LoadRequest{Reference: summaries[0].Reference, Resources: []string{"references/fields.md", "assets/icon.bin"}})
	if err != nil || len(loaded.Resources) != 2 || string(loaded.Resources[0].Data) != "Only report relevant fields." || len(loaded.Resources[1].Data) != 4 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}

	restarted, err := Open(configuration, Options{})
	if err != nil || restarted.Count() != 1 {
		t.Fatalf("restart catalog=%+v err=%v", restarted, err)
	}
	reloaded, err := restarted.Load(context.Background(), skill.LoadRequest{Reference: summaries[0].Reference})
	if err != nil || string(reloaded.Instructions) != string(loaded.Instructions) {
		t.Fatalf("restart load=%+v err=%v", reloaded, err)
	}
}

func TestLocalCatalogRejectsMissingEscapingAndChangedResources(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "safe-skill", skillDocument("safe-skill", "Use safe local references when needed.", "Read references/guide.md."), map[string][]byte{"references/guide.md": []byte("guide")})
	catalog, err := Open(testConfiguration(root), Options{})
	if err != nil {
		t.Fatal(err)
	}
	reference := skill.Reference{ID: "safe-skill", Revision: 3}
	for _, resource := range []string{"missing.md", "../outside.md"} {
		if _, err := catalog.Load(context.Background(), skill.LoadRequest{Reference: reference, Resources: []string{resource}}); err == nil {
			t.Fatalf("resource %q unexpectedly loaded", resource)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "safe-skill", "references", "escape.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(context.Background(), skill.LoadRequest{Reference: reference, Resources: []string{"references/escape.md"}}); !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("symlink escape error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "safe-skill", "SKILL.md"), []byte(skillDocument("safe-skill", "Changed after discovery and must be rejected.", "changed")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(context.Background(), skill.LoadRequest{Reference: reference}); !errors.Is(err, skill.ErrIntegrity) {
		t.Fatalf("changed instructions error = %v", err)
	}
}

func TestLocalCatalogEnforcesBoundsAndRelativeRoot(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "skills")
	writeSkill(t, root, "bounded", skillDocument("bounded", "Bounded instructions for a deterministic test.", "123456789"), map[string][]byte{"large.txt": []byte("12345")})
	configuration := testConfiguration("skills")
	configuration.MaxInstructionBytes = 8
	if _, err := Open(configuration, Options{DataRoot: dataRoot}); !errors.Is(err, skill.ErrTransferBounds) {
		t.Fatalf("instruction bound error = %v", err)
	}
	configuration.MaxInstructionBytes = 1024
	configuration.MaxResourceBytes = 4
	catalog, err := Open(configuration, Options{DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(context.Background(), skill.LoadRequest{Reference: skill.Reference{ID: "bounded", Revision: 3}, Resources: []string{"large.txt"}}); !errors.Is(err, skill.ErrTransferBounds) {
		t.Fatalf("resource bound error = %v", err)
	}
	escape := configuration
	escape.Directory = "../skills"
	if _, err := Open(escape, Options{DataRoot: dataRoot}); !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestCatalogRejectsDuplicateIdentifiers(t *testing.T) {
	catalog := &Catalog{entries: map[skill.ID]entry{}}
	loaded := entry{descriptor: skill.Descriptor{ID: "duplicate"}}
	if err := catalog.add(loaded); err != nil {
		t.Fatal(err)
	}
	if err := catalog.add(loaded); !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("duplicate error = %v", err)
	}
}
