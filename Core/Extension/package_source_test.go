package extension

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	packagesource "github.com/Neon-Dolls/neondoll/Core/PackageSource"
	packagesourcelocal "github.com/Neon-Dolls/neondoll/Core/PackageSource/Local"
)

func packageSourceManifest(t *testing.T, root, id string) Manifest {
	t.Helper()
	path := filepath.Join(findRepoRoot(t), "Core/Extension/testdata/package_source.wasm")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return Manifest{Version: ManifestVersion, ExtensionID: "synthetic-package-source", InstanceID: id, Contract: ContractPackageSource, ABI: PackageSourceABIVersion, Enabled: true, Artifact: Artifact{Path: relative, SHA256: fmt.Sprintf("%x", digest)}}
}

func TestLocalBootstrapInstallsExternalWASMPackageSource(t *testing.T) {
	ctx := context.Background()
	repoRoot, dataRoot := findRepoRoot(t), t.TempDir()
	artifact, err := os.ReadFile(filepath.Join(repoRoot, "Core/Extension/testdata/package_source.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	packagesRoot := filepath.Join(dataRoot, "packages", "local")
	if err = os.MkdirAll(packagesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(packagesRoot, "external.wasm"), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	extensionManifest := Manifest{Version: 1, ExtensionID: "external-source", InstanceID: "external-source", Contract: ContractPackageSource, ABI: PackageSourceABIVersion, Enabled: true, Artifact: Artifact{Path: "module.wasm", SHA256: fmt.Sprintf("%x", digest)}, Requested: []Capability{CapabilityState}, Granted: []Capability{CapabilityState}}
	rawExtension, _ := json.Marshal(extensionManifest)
	packageManifest := map[string]any{"version": 1, "id": "external-source", "package_version": "1", "kind": "extension", "name": "External Source", "description": "independent source", "artifact_path": "external.wasm", "artifact_sha256": fmt.Sprintf("%x", digest), "artifact_media_type": "application/wasm", "extension_manifest": json.RawMessage(rawExtension)}
	rawPackage, _ := json.Marshal(packageManifest)
	if err = os.WriteFile(filepath.Join(packagesRoot, "external.json"), rawPackage, 0o600); err != nil {
		t.Fatal(err)
	}
	localSource, err := packagesourcelocal.Open(dataRoot, packagesource.DefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	registry := packagesource.NewRegistry()
	if err = registry.Register(localSource); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(dataRoot, "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(runtime, &testHost{ingress: &testIngress{}}, dataRoot, nil, WithPackageSourceRegistry(registry))
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := packagesource.NewExecutor(registry, nil, manager, packagesource.ArtifactBounds{MaxBytes: 16 << 20, MaxItems: 4})
	search, err := localSource.Search(ctx, packagesource.SearchRequest{Limit: 10})
	if err != nil || len(search.Packages) != 1 {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	plan, err := localSource.Plan(ctx, packagesource.PlanRequest{Package: search.Packages[0].Reference, Operation: "install"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := executor.Execute(ctx, plan)
	if err != nil || !report.Completed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, ok := registry.Source("external-source"); !ok {
		t.Fatalf("external source not registered: %v", registry.IDs())
	}
	installedManifest, err := os.ReadFile(filepath.Join(dataRoot, "extensions", "installed", "external-source", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var staged Manifest
	if json.Unmarshal(installedManifest, &staged) != nil || len(staged.Granted) != 0 {
		t.Fatalf("Package Source granted itself capabilities: %+v", staged.Granted)
	}
	if err = manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restartedRegistry := packagesource.NewRegistry()
	restartedLocal, err := packagesourcelocal.Open(dataRoot, packagesource.DefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if err = restartedRegistry.Register(restartedLocal); err != nil {
		t.Fatal(err)
	}
	restartedRuntime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(dataRoot, "state-restarted")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(restartedRuntime, &testHost{ingress: &testIngress{}}, dataRoot, nil, WithPackageSourceRegistry(restartedRegistry))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(ctx)
	if err = restarted.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := restartedRegistry.Source("external-source"); !ok {
		t.Fatalf("installed source not reconstructed: %v", restartedRegistry.IDs())
	}
}

func TestIndependentWASMPackageSourceContract(t *testing.T) {
	ctx := context.Background()
	root := findRepoRoot(t)
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest := packageSourceManifest(t, root, "wasm-source")
	if err = runtime.Load(ctx, manifest, root); err != nil {
		t.Fatal(err)
	}
	source := NewWASMPackageSource(manifest, runtime)
	search, err := source.Search(ctx, packagesource.SearchRequest{Limit: 10})
	if err != nil || len(search.Packages) != 1 || search.Packages[0].Reference.SourceID != "wasm-source" {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	pendingState, err := runtime.state.Get(ctx, "wasm-source", packageSourceProposalStateKey)
	if err != nil || !bytes.Contains(pendingState, []byte("proposal-1")) {
		t.Fatalf("proposal emitted during search was not durable: %q err=%v", pendingState, err)
	}
	recoveredSource := NewWASMPackageSource(manifest, runtime)
	recoveredProposals, err := recoveredSource.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(recoveredProposals.Proposals) != 1 {
		t.Fatalf("recovered proposals=%+v err=%v", recoveredProposals, err)
	}
	plan, err := source.Plan(ctx, packagesource.PlanRequest{Package: search.Packages[0].Reference, Operation: "install"})
	if err != nil || plan.SourceID != "wasm-source" || plan.Items[0].Package.SourceID != "wasm-source" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	var artifact bytes.Buffer
	if err = source.Acquire(ctx, plan.Items[0].Artifact, &artifact); err != nil || artifact.String() != "abc" {
		t.Fatalf("artifact=%q err=%v", artifact.String(), err)
	}
	workflow, err := source.RequestWorkflow(ctx, packagesource.WorkflowRequest{Package: search.Packages[0].Reference, Operation: "install", Correlation: "turn-1"})
	if err != nil || workflow.State != packagesource.WorkflowReady {
		t.Fatalf("workflow=%+v err=%v", workflow, err)
	}
	status, err := source.WorkflowStatus(ctx, workflow.ID)
	if err != nil || status.State != packagesource.WorkflowReady {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	cancelled, err := source.CancelWorkflow(ctx, workflow.ID)
	if err != nil || cancelled.State != packagesource.WorkflowCancelled {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	proposals, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 || proposals.Proposals[0].Plan.SourceID != "wasm-source" {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
	if err = source.AcknowledgeProposals(ctx, []string{"proposal-1"}); err != nil {
		t.Fatal(err)
	}
	pendingState, err = runtime.state.Get(ctx, "wasm-source", packageSourceProposalStateKey)
	if err != nil || bytes.Contains(pendingState, []byte("proposal-1")) {
		t.Fatalf("acknowledged proposal retained: %q err=%v", pendingState, err)
	}
}

func TestManagerRegistersMultipleWASMPackageSources(t *testing.T) {
	ctx := context.Background()
	root := findRepoRoot(t)
	registry := packagesource.NewRegistry()
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := &testHost{ingress: &testIngress{}}
	manager, err := NewManager(runtime, host, root, []Manifest{packageSourceManifest(t, root, "source-one"), packageSourceManifest(t, root, "source-two")}, WithPackageSourceRegistry(registry))
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	if ids := registry.IDs(); len(ids) != 2 || ids[0] != "source-one" || ids[1] != "source-two" {
		t.Fatalf("sources=%v", ids)
	}
	if err = manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(registry.IDs()) != 0 {
		t.Fatalf("sources remain after shutdown: %v", registry.IDs())
	}
}

func TestWASMPackageSourceConcurrentSearchAndProposalPollDoNotLoseEvents(t *testing.T) {
	ctx := context.Background()
	root := findRepoRoot(t)
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest := packageSourceManifest(t, root, "wasm-source")
	if err = runtime.Load(ctx, manifest, root); err != nil {
		t.Fatal(err)
	}
	source := NewWASMPackageSource(manifest, runtime)
	var wait sync.WaitGroup
	wait.Add(2)
	errorsFound := make(chan error, 2)
	go func() {
		defer wait.Done()
		_, err := source.Search(ctx, packagesource.SearchRequest{Limit: 10})
		errorsFound <- err
	}()
	go func() {
		defer wait.Done()
		_, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
		errorsFound <- err
	}()
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	proposals, err := source.Proposals(ctx, packagesource.ProposalRequest{Limit: 10})
	if err != nil || len(proposals.Proposals) != 1 {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
}
