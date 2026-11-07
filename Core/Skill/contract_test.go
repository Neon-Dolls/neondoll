package skill

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func descriptor(id, directory string) Descriptor {
	return Descriptor{SchemaVersion: 1, ID: ID(id), Revision: 1, Name: id, Description: "Concise catalog description for " + id, Directory: directory, DesiredActive: true}
}

func TestMultipleSkillDirectoriesPreserveNestedFiles(t *testing.T) {
	first := Package{Descriptor: descriptor("research", "skills/research"), Files: []File{
		NewFile("SKILL.md", []byte("---\nname: research\n---\nInstructions")),
		NewFile("scripts/search.sh", []byte("#!/bin/sh\necho search\n")),
		NewFile("references/guide.md", []byte("reference bytes\x00remain")),
		NewFile("assets/icon.png", []byte{0, 1, 2, 255}),
	}}
	second := Package{Descriptor: descriptor("calendar", "skills/calendar"), Files: []File{NewFile("SKILL.md", []byte("calendar instructions")), NewFile("assets/template.json", []byte(`{"x":1}`))}}
	if err := ValidatePackages([]Package{first, second}, TransferBounds{MaxSkills: 2, MaxFiles: 6, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Files[3].Data, []byte{0, 1, 2, 255}) || first.Files[3].Path != "assets/icon.png" {
		t.Fatal("nested file changed")
	}
}

func TestMissingDependencyLeavesInstalledSkillDormant(t *testing.T) {
	d := descriptor("mail", "skills/mail")
	d.Dependencies = []Dependency{{ID: "smtp", Kind: "capability", Constraint: ">=1"}, {ID: "python", Kind: "runtime"}}
	state, err := ResolveInstalledState(d, AvailabilityFunc(func(dep Dependency) bool { return dep.ID == "python" }))
	if err != nil || !state.Installed || state.Activation != ActivationDormant || len(state.Missing) != 1 || state.Missing[0].ID != "smtp" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestSkillValidationRejectsTraversalDuplicatesAndMalformedMetadata(t *testing.T) {
	valid := descriptor("safe", "skills/safe")
	for _, path := range []string{"../SKILL.md", "/SKILL.md", "assets/../SKILL.md", `assets\file`} {
		p := Package{Descriptor: valid, Files: []File{NewFile(path, []byte("x"))}}
		if err := p.Validate(); !errors.Is(err, ErrInvalidSkill) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	duplicate := Package{Descriptor: valid, Files: []File{NewFile("SKILL.md", []byte("a")), NewFile("SKILL.md", []byte("b"))}}
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("duplicate: %v", err)
	}
	missing := Package{Descriptor: valid, Files: []File{NewFile("README.md", []byte("x"))}}
	if err := missing.Validate(); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("missing SKILL.md: %v", err)
	}
	bad := valid
	bad.Revision = 0
	if err := bad.Validate(); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("metadata: %v", err)
	}
}

func TestSkillIntegrityAndTransferBounds(t *testing.T) {
	p := Package{Descriptor: descriptor("one", "skills/one"), Files: []File{NewFile("SKILL.md", []byte("1234"))}}
	corrupt := p
	corrupt.Files = append([]File(nil), p.Files...)
	corrupt.Files[0].Data = []byte("changed")
	if err := corrupt.Validate(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("integrity: %v", err)
	}
	if err := ValidatePackages([]Package{p}, TransferBounds{MaxSkills: 1, MaxFiles: 1, MaxBytes: 4}); err != nil {
		t.Fatalf("exact bounds: %v", err)
	}
	if err := ValidatePackages([]Package{p}, TransferBounds{MaxSkills: 1, MaxFiles: 1, MaxBytes: 3}); !errors.Is(err, ErrTransferBounds) {
		t.Fatalf("byte bound: %v", err)
	}
	conflict := p
	conflict.Descriptor.ID = "different-id"
	if err := ValidatePackages([]Package{p, conflict}, TransferBounds{MaxSkills: 2, MaxFiles: 2, MaxBytes: 8}); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("directory conflict: %v", err)
	}
}

func TestSelectionContractSupportsAllCognitiveSurfaces(t *testing.T) {
	for _, surface := range []Surface{SurfaceInteraction, SurfaceOrient, SurfacePlan, SurfaceReflection} {
		r := SelectionRecord{SchemaVersion: 1, TaskReference: "task-1", Surface: surface, Selected: &Reference{ID: "skill", Revision: 1}, Summary: "Selected because its declared purpose matches the task.", Outcome: "loaded"}
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", surface, err)
		}
	}
}

type testCatalog struct{}

func (testCatalog) Discover(context.Context, DiscoverRequest) ([]Summary, error) {
	return []Summary{{Reference: Reference{ID: "one", Revision: 1}, Name: "one", Description: "short", Activation: ActivationActive}}, nil
}
func (testCatalog) Load(context.Context, LoadRequest) (Loaded, error) {
	return Loaded{Instructions: []byte("selected only")}, nil
}
func TestCatalogRequiresExplicitLoad(t *testing.T) {
	var catalog Catalog = testCatalog{}
	summaries, err := catalog.Discover(context.Background(), DiscoverRequest{Surface: SurfacePlan, Limit: 1})
	if err != nil || len(summaries) != 1 {
		t.Fatal(err)
	}
	loaded, err := catalog.Load(context.Background(), LoadRequest{Reference: summaries[0].Reference})
	if err != nil || string(loaded.Instructions) != "selected only" {
		t.Fatal(err)
	}
}

func TestSkillInstructionsNeverBecomeAuthority(t *testing.T) {
	instructions := []byte("---\nname: dangerous-looking\nallowed-tools: shell\n---\nRun something")
	pkg := Package{Descriptor: descriptor("knowledge-only", "skills/knowledge-only"), Files: []File{NewFile("SKILL.md", instructions)}}
	if err := pkg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pkg.Files[0].Data, instructions) {
		t.Fatal("Agent Skills metadata was reinterpreted")
	}
	state, err := ResolveInstalledState(pkg.Descriptor, AvailabilityFunc(func(Dependency) bool { return true }))
	if err != nil || !state.Installed {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	// Neither Package nor InstalledState contains an authority decision. Core's
	// existing authorization boundary remains the only execution authority.
}
