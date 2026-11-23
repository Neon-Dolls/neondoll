package local

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	packagesource "github.com/Neon-Dolls/neondoll/Core/PackageSource"
	"github.com/Neon-Dolls/neondoll/Core/Skill"
	skilllocal "github.com/Neon-Dolls/neondoll/Core/Skill/Local"
)

func skillZIP(t *testing.T, id, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: " + id + "\ndescription: Acquired offline Skill for deterministic tests.\n---\n\n" + body))
	resource, _ := writer.Create("references/guide.md")
	_, _ = resource.Write([]byte("supporting guidance"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writePackage(t *testing.T, root, id, version string, artifact []byte, proposal bool) {
	t.Helper()
	packages := filepath.Join(root, "packages", "local")
	if err := os.MkdirAll(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactName := id + "-" + version + ".zip"
	if err := os.WriteFile(filepath.Join(packages, artifactName), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	manifest := map[string]any{"version": 1, "id": id, "package_version": version, "kind": "skill", "name": id, "description": "offline package", "artifact_path": artifactName, "artifact_sha256": hex.EncodeToString(digest[:]), "artifact_media_type": "application/vnd.neondoll.skill+zip", "proposal": proposal, "proposal_reason": "local recommendation"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(packages, id+"-"+version+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSourcePlanInstallsAndRefreshesSkillCatalog(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePackage(t, root, "offline-skill", "1", skillZIP(t, "offline-skill", "Use runtime.info."), true)
	configuration := packagesource.DefaultConfiguration()
	source, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	search, err := source.Search(ctx, packagesource.SearchRequest{Query: "offline", Limit: 10})
	if err != nil || len(search.Packages) != 1 {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	plan, err := source.Plan(ctx, packagesource.PlanRequest{Package: search.Packages[0].Reference, Operation: "install"})
	if err != nil {
		t.Fatal(err)
	}
	skillConfiguration := skill.DefaultConfiguration()
	skillConfiguration.Directory = "skills"
	catalog, err := skilllocal.Open(skillConfiguration, skilllocal.Options{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	registry := packagesource.NewRegistry()
	if err = registry.Register(source); err != nil {
		t.Fatal(err)
	}
	executor, err := packagesource.NewExecutor(registry, catalog, nil, packagesource.ArtifactBounds{MaxBytes: configuration.MaxArtifactBytes, MaxItems: configuration.MaxPlanItems})
	if err != nil {
		t.Fatal(err)
	}
	report, err := executor.Execute(ctx, plan)
	if err != nil || !report.Completed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	summaries, err := catalog.Discover(ctx, skill.DiscoverRequest{Limit: 10})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("catalog=%+v err=%v", summaries, err)
	}
	loaded, err := catalog.Load(ctx, skill.LoadRequest{Reference: summaries[0].Reference, Resources: []string{"references/guide.md"}})
	if err != nil || !bytes.Contains(loaded.Instructions, []byte("runtime.info")) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	restarted, err := skilllocal.Open(skillConfiguration, skilllocal.Options{DataRoot: root})
	if err != nil || restarted.Count() != 1 {
		t.Fatalf("restart count=%d err=%v", restarted.Count(), err)
	}
	proposals, err := source.Proposals(ctx, packagesource.ProposalRequest{Cursor: "zzzz-opaque", Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
	if err = source.AcknowledgeProposals(ctx, []string{proposals.Proposals[0].ID}); err != nil {
		t.Fatal(err)
	}
	afterAck, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(afterAck.Proposals) != 0 {
		t.Fatalf("acknowledged proposals=%+v err=%v", afterAck, err)
	}
	workflow, err := source.RequestWorkflow(ctx, packagesource.WorkflowRequest{Package: search.Packages[0].Reference, Operation: "install", Correlation: "turn-1"})
	if err != nil || workflow.State != packagesource.WorkflowReady || workflow.Plan == nil {
		t.Fatalf("workflow=%+v err=%v", workflow, err)
	}
	status, err := source.WorkflowStatus(ctx, workflow.ID)
	if err != nil || status.State != packagesource.WorkflowReady {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	cancelled, err := source.CancelWorkflow(ctx, workflow.ID)
	if err != nil || cancelled.State != packagesource.WorkflowCancelled || cancelled.Plan != nil {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
}

func TestFailedUpdatePreservesInstalledSkill(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "skills"), 0o700)
	writePackage(t, root, "stable-skill", "1", skillZIP(t, "stable-skill", "original"), false)
	writePackage(t, root, "stable-skill", "2", []byte("not a zip"), false)
	configuration := packagesource.DefaultConfiguration()
	source, _ := Open(root, configuration)
	registry := packagesource.NewRegistry()
	_ = registry.Register(source)
	skillConfiguration := skill.DefaultConfiguration()
	skillConfiguration.Directory = "skills"
	catalog, _ := skilllocal.Open(skillConfiguration, skilllocal.Options{DataRoot: root})
	executor, _ := packagesource.NewExecutor(registry, catalog, nil, packagesource.ArtifactBounds{MaxBytes: configuration.MaxArtifactBytes, MaxItems: 10})
	plan, _ := source.Plan(ctx, packagesource.PlanRequest{Package: packagesource.Reference{SourceID: SourceID, ID: "stable-skill", Version: "1"}, Operation: "install"})
	if _, err := executor.Execute(ctx, plan); err != nil {
		t.Fatal(err)
	}
	badPlan, _ := source.Plan(ctx, packagesource.PlanRequest{Package: packagesource.Reference{SourceID: SourceID, ID: "stable-skill", Version: "2"}, Operation: "update"})
	if _, err := executor.Execute(ctx, badPlan); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	summaries, _ := catalog.Discover(ctx, skill.DiscoverRequest{Limit: 10})
	loaded, err := catalog.Load(ctx, skill.LoadRequest{Reference: summaries[0].Reference})
	if err != nil || !bytes.Contains(loaded.Instructions, []byte("original")) {
		t.Fatalf("existing Skill lost: %q err=%v", loaded.Instructions, err)
	}
}

func TestProposalAcknowledgmentsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writePackage(t, root, "proposed-skill", "1", skillZIP(t, "proposed-skill", "Use runtime.info."), true)
	configuration := packagesource.DefaultConfiguration()

	source, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("initial proposals=%+v err=%v", proposals, err)
	}
	proposalID := proposals.Proposals[0].ID
	if err = source.AcknowledgeProposals(ctx, []string{proposalID}); err != nil {
		t.Fatal(err)
	}
	if err = source.AcknowledgeProposals(ctx, []string{proposalID}); err != nil {
		t.Fatalf("repeated acknowledgment: %v", err)
	}
	afterAck, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(afterAck.Proposals) != 0 {
		t.Fatalf("after acknowledgment proposals=%+v err=%v", afterAck, err)
	}
	if err = source.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := reopened.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(afterRestart.Proposals) != 0 {
		t.Fatalf("after restart proposals=%+v err=%v", afterRestart, err)
	}
}

func TestUnacknowledgedProposalSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writePackage(t, root, "pending-skill", "1", skillZIP(t, "pending-skill", "Use runtime.info."), true)
	configuration := packagesource.DefaultConfiguration()
	source, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if proposals, proposalErr := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10}); proposalErr != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("initial proposals=%+v err=%v", proposals, proposalErr)
	}
	if err = source.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := reopened.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("restarted proposals=%+v err=%v", proposals, err)
	}
}

func TestProposalAcknowledgmentPersistenceFailureDoesNotAcknowledge(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writePackage(t, root, "durable-skill", "1", skillZIP(t, "durable-skill", "Use runtime.info."), true)
	configuration := packagesource.DefaultConfiguration()
	source, err := Open(root, configuration)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("initial proposals=%+v err=%v", proposals, err)
	}
	statePath := filepath.Join(root, configuration.LocalDirectory, proposalAcknowledgmentsFile)
	if err = os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = source.AcknowledgeProposals(ctx, []string{proposals.Proposals[0].ID}); err == nil {
		t.Fatal("acknowledgment succeeded without durable persistence")
	}
	stillPending, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(stillPending.Proposals) != 1 {
		t.Fatalf("proposal silently acknowledged: proposals=%+v err=%v", stillPending, err)
	}
	if err = os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err = source.AcknowledgeProposals(ctx, []string{proposals.Proposals[0].ID}); err != nil {
		t.Fatalf("acknowledgment did not recover: %v", err)
	}
}

func TestLocalSourceRejectsTraversalSymlinkIntegrityAndOversize(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string, packagesource.Configuration)
	}{
		{name: "traversal", prepare: func(t *testing.T, root string, _ packagesource.Configuration) {
			writeRawManifest(t, root, "../outside", strings.Repeat("0", 64))
		}},
		{name: "symlink", prepare: func(t *testing.T, root string, _ packagesource.Configuration) {
			packages := filepath.Join(root, "packages", "local")
			_ = os.MkdirAll(packages, 0o700)
			outside := filepath.Join(t.TempDir(), "artifact")
			_ = os.WriteFile(outside, []byte("outside"), 0o600)
			_ = os.Symlink(outside, filepath.Join(packages, "artifact.zip"))
			digest := sha256.Sum256([]byte("outside"))
			writeRawManifest(t, root, "artifact.zip", hex.EncodeToString(digest[:]))
		}},
		{name: "integrity", prepare: func(t *testing.T, root string, _ packagesource.Configuration) {
			packages := filepath.Join(root, "packages", "local")
			_ = os.MkdirAll(packages, 0o700)
			_ = os.WriteFile(filepath.Join(packages, "artifact.zip"), []byte("data"), 0o600)
			writeRawManifest(t, root, "artifact.zip", strings.Repeat("0", 64))
		}},
		{name: "oversize", prepare: func(t *testing.T, root string, configuration packagesource.Configuration) {
			packages := filepath.Join(root, "packages", "local")
			_ = os.MkdirAll(packages, 0o700)
			data := bytes.Repeat([]byte("x"), int(configuration.MaxArtifactBytes+1))
			_ = os.WriteFile(filepath.Join(packages, "artifact.zip"), data, 0o600)
			digest := sha256.Sum256(data)
			writeRawManifest(t, root, "artifact.zip", hex.EncodeToString(digest[:]))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configuration := packagesource.DefaultConfiguration()
			configuration.MaxArtifactBytes = 64
			test.prepare(t, root, configuration)
			if _, err := Open(root, configuration); err == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
}

func writeRawManifest(t *testing.T, root, artifactPath, digest string) {
	t.Helper()
	packages := filepath.Join(root, "packages", "local")
	if err := os.MkdirAll(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"version": 1, "id": "unsafe", "package_version": "1", "kind": "skill", "name": "unsafe", "description": "unsafe fixture", "artifact_path": artifactPath, "artifact_sha256": digest, "artifact_media_type": "application/zip"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(packages, "unsafe.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
