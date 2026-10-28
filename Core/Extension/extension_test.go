package extension

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type testIngress struct {
	mu            sync.Mutex
	messages      []gateway.VerifiedSender
	events        []*events.Event
	responses     []*events.Event
	failures      int
	attempts      int
	handleEntered chan struct{}
	handleRelease chan struct{}
}

func (*testIngress) ResolvePlatformSession(context.Context, string) (session.ID, error) {
	return "session-1", nil
}
func (*testIngress) ExternalConversationRef(context.Context, session.ID) (string, error) {
	return "synthetic/chat/7", nil
}
func (i *testIngress) EmitInteractionResponse(_ context.Context, _ session.ID, response *events.Event) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.responses = append(i.responses, response)
	return nil
}
func (i *testIngress) HandleLegacy(_ context.Context, _ session.ID, s gateway.VerifiedSender, event *events.Event) (*events.Event, error) {
	i.mu.Lock()
	i.attempts++
	if i.failures > 0 {
		i.failures--
		i.mu.Unlock()
		return nil, errors.New("transient ingress failure")
	}
	i.mu.Unlock()
	if i.handleEntered != nil {
		select {
		case <-i.handleEntered:
		default:
			close(i.handleEntered)
		}
	}
	if i.handleRelease != nil {
		<-i.handleRelease
	}
	i.mu.Lock()
	i.messages = append(i.messages, s)
	if event != nil {
		copy := *event
		i.events = append(i.events, &copy)
	}
	i.mu.Unlock()
	return nil, nil
}
func (i *testIngress) HandlePlatformMessage(ctx context.Context, _ string, s gateway.VerifiedSender, e *events.Event) (*gateway.PlatformResponse, error) {
	_, err := i.HandleLegacy(ctx, "session-1", s, e)
	if err != nil {
		return nil, err
	}
	response := events.NewResponse(e.ID, e.DollID, "ok")
	return &gateway.PlatformResponse{SessionID: "session-1", Event: &response}, nil
}
func (*testIngress) HandleSessionEvent(context.Context, gateway.VerifiedSender, sessionevent.Event) (*permissionrequest.Request, error) {
	return nil, nil
}

type testHost struct {
	mu                sync.Mutex
	ingress           *testIngress
	adapters          map[string]gateway.Adapter
	registers         int
	registerEntered   chan struct{}
	registerRelease   chan struct{}
	unregisterEntered chan struct{}
	unregisterRelease chan struct{}
}

func (h *testHost) Register(ctx context.Context, a gateway.Adapter) error {
	h.mu.Lock()
	h.registers++
	if h.adapters == nil {
		h.adapters = map[string]gateway.Adapter{}
	}
	if h.adapters[string(a.InstanceID())] != nil {
		h.mu.Unlock()
		return gateway.ErrAlreadyRegistered
	}
	h.adapters[string(a.InstanceID())] = a
	h.mu.Unlock()
	if h.registerEntered != nil {
		select {
		case <-h.registerEntered:
		default:
			close(h.registerEntered)
		}
	}
	if h.registerRelease != nil {
		<-h.registerRelease
	}
	return a.Start(ctx, h.ingress)
}
func (h *testHost) Unregister(ctx context.Context, id externalidentity.GatewayInstanceID) error {
	if h.unregisterEntered != nil {
		select {
		case <-h.unregisterEntered:
		default:
			close(h.unregisterEntered)
		}
	}
	if h.unregisterRelease != nil {
		<-h.unregisterRelease
	}
	h.mu.Lock()
	a := h.adapters[string(id)]
	delete(h.adapters, string(id))
	h.mu.Unlock()
	if a == nil {
		return gateway.ErrUnavailable
	}
	return a.Stop(ctx)
}

func testManifest(t *testing.T, id string, grants ...Capability) Manifest {
	t.Helper()
	wasm, err := os.ReadFile("testdata/gateway.wasm")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	requested := []Capability{CapabilityState}
	requested = append(requested, grants...)
	return Manifest{Version: 1, ExtensionID: "synthetic", InstanceID: id, Contract: ContractGateway, ABI: 2, Enabled: true, Artifact: Artifact{Path: "Core/Extension/testdata/gateway.wasm", SHA256: fmt.Sprintf("%x", sum)}, Configuration: json.RawMessage(`{"version":1}`), Requested: requested, Granted: grants, Gateway: &GatewayConfiguration{Platform: "synthetic", DollID: "doll", ConversationPrefix: "synthetic/chat/"}}
}

func TestManagerLoadsIndependentGatewayArtifactsAndDelivers(t *testing.T) {
	ctx := context.Background()
	root := findRepoRoot(t)
	state := NewFileStateStore(filepath.Join(t.TempDir(), "state"))
	runtime, err := NewWASMRuntime(ctx, state, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ingress := &testIngress{}
	host := &testHost{ingress: ingress}
	first, second := testManifest(t, "one"), testManifest(t, "two")
	manager, err := NewManager(runtime, host, root, []Manifest{first, second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	if err := manager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		ingress.mu.Lock()
		count := len(ingress.messages)
		ingress.mu.Unlock()
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbound messages=%d", count)
		}
		time.Sleep(time.Millisecond)
	}
	event, err := sessionevent.NewEvent("session-1", sessionevent.TypeMessage, sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartText, Text: "hello"}}}, sessionevent.Correlation{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	delivery := sessionevent.Delivery{ID: "delivery-1", Event: event, Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "one"}}
	envelope, _ := gateway.NewOutboundEnvelope(delivery)
	host.mu.Lock()
	adapter := host.adapters["one"]
	host.mu.Unlock()
	if err := adapter.Deliver(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Get(ctx, "one", "probe"); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("unauthorized state call succeeded: %v", err)
	}
	if _, err := state.Get(ctx, "two", "delivery/delivery-1"); !errors.Is(err, ErrStateNotFound) {
		t.Fatal("installation state leaked")
	}
	if err := manager.Disable(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	delivery.ID = "delivery-after-disable"
	envelope, _ = gateway.NewOutboundEnvelope(delivery)
	if err := adapter.Deliver(ctx, envelope); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("delivery after disable = %v", err)
	}
}

func TestManifestValidationAndDurableStateRestart(t *testing.T) {
	bad := testManifest(t, "bad")
	bad.ABI = 99
	if !errors.Is(bad.Validate(), ErrUnsupportedABI) {
		t.Fatal("incompatible ABI accepted")
	}
	bad = testManifest(t, "bad")
	bad.Contract = ContractCapability
	if !errors.Is(bad.Validate(), ErrUnsupportedContract) {
		t.Fatal("reserved contract accepted")
	}
	root := t.TempDir()
	first := NewFileStateStore(root)
	if err := first.Put(context.Background(), "instance", "cursor", []byte("42")); err != nil {
		t.Fatal(err)
	}
	second := NewFileStateStore(root)
	value, err := second.Get(context.Background(), "instance", "cursor")
	if err != nil || string(value) != "42" {
		t.Fatalf("restart state=%q err=%v", value, err)
	}
	runtime, err := NewWASMRuntime(context.Background(), second, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	bad = testManifest(t, "integrity")
	bad.Artifact.SHA256 = fmt.Sprintf("%064d", 0)
	if err := runtime.Load(context.Background(), bad, findRepoRoot(t)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("integrity error = %v", err)
	}
}

func TestRuntimeSupportsBoundedStructuredEventsBeyondLegacyBuffer(t *testing.T) {
	ctx := context.Background()
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(t.TempDir()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest := testManifest(t, "large-event")
	if err = runtime.Load(ctx, manifest, findRepoRoot(t)); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"text": strings.Repeat("界", 4096)})
	if len(payload) <= minGuestEventBuffer {
		t.Fatalf("test payload bytes=%d", len(payload))
	}
	if err = runtime.HandleEvent(manifest.InstanceID, "session.event", payload); err != nil {
		t.Fatalf("valid structured event: %v", err)
	}
	tooLarge, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", maxGuestEvent)})
	if err = runtime.HandleEvent(manifest.InstanceID, "session.event", tooLarge); err == nil {
		t.Fatal("oversized structured event accepted")
	}
}

func TestGuestCallOutboundIsAtomicAndRecovers(t *testing.T) {
	ctx := context.Background()
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(t.TempDir()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest := testManifest(t, "atomic-output")
	if err = runtime.Load(ctx, manifest, findRepoRoot(t)); err != nil {
		t.Fatal(err)
	}

	// Leave one successful result undrained. The failed call must discard both
	// that stale result and its own partial output.
	if err = runtime.HandleEvent(manifest.InstanceID, "test.success", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"test.partial_invalid", "test.partial_oversized", "test.partial_excessive"} {
		if err = runtime.HandleEvent(manifest.InstanceID, typ, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("%s succeeded", typ)
		}
		if output := runtime.TakeOutbound(manifest.InstanceID); len(output) != 0 {
			t.Fatalf("%s leaked %d partial or stale events", typ, len(output))
		}
		if err = runtime.HandleEvent(manifest.InstanceID, "test.success", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("successful call after %s: %v", typ, err)
		}
		output := runtime.TakeOutbound(manifest.InstanceID)
		if len(output) != 1 || string(output[0].Payload) != `{"event_type":"test.result","ok":true}` {
			t.Fatalf("successful output after %s = %+v", typ, output)
		}
	}

	// Successful calls continue to publish exactly their own result. Leaving
	// one undrained cannot duplicate it into the following successful call.
	if err = runtime.HandleEvent(manifest.InstanceID, "test.success", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = runtime.HandleEvent(manifest.InstanceID, "test.success", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	output := runtime.TakeOutbound(manifest.InstanceID)
	if len(output) != 1 || string(output[0].Payload) != `{"event_type":"test.result","ok":true}` {
		t.Fatalf("consecutive successful output = %+v", output)
	}
}

func TestGatewayInboundPreservesMediaReferences(t *testing.T) {
	ingress := &testIngress{}
	manifest := testManifest(t, "media")
	adapter := NewGatewayAdapter(manifest, nil)
	adapter.ingress, adapter.routes = ingress, ingress
	in := gatewayIncoming{
		UpdateID: 7, ChatID: 77, SenderID: 9, Text: "caption",
		Media: []events.MediaReference{{Type: "image", MediaID: "provider-file", UniqueID: "stable-file", SizeBytes: 42, Caption: "caption", MessageID: "70", ConversationID: "77"}},
	}
	if err := adapter.handleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	ingress.mu.Lock()
	defer ingress.mu.Unlock()
	if len(ingress.events) != 1 {
		t.Fatalf("events=%d", len(ingress.events))
	}
	if len(ingress.responses) != 1 || ingress.responses[0].CorrelationID != "media:7" {
		t.Fatalf("responses=%#v", ingress.responses)
	}
	payload, ok := ingress.events[0].Payload.(events.MessagePayload)
	if !ok || payload.Text != "caption" || len(payload.Media) != 1 || payload.Media[0].MediaID != "provider-file" {
		t.Fatalf("payload=%#v", ingress.events[0].Payload)
	}
}

func TestInboundFailurePersistsAndRetriesAfterRestart(t *testing.T) {
	ctx, root, stateRoot := context.Background(), findRepoRoot(t), filepath.Join(t.TempDir(), "state")
	manifest := testManifest(t, "retry")
	firstRuntime, _ := NewWASMRuntime(ctx, NewFileStateStore(stateRoot), t.TempDir())
	failing := &testIngress{failures: 100}
	firstHost := &testHost{ingress: failing}
	firstManager, err := NewManager(firstRuntime, firstHost, root, []Manifest{manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err = firstManager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		failing.mu.Lock()
		attempts := failing.attempts
		failing.mu.Unlock()
		if attempts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ingress was not attempted")
		}
		time.Sleep(time.Millisecond)
	}
	if err = firstManager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	secondRuntime, _ := NewWASMRuntime(ctx, NewFileStateStore(stateRoot), t.TempDir())
	succeeding := &testIngress{}
	secondManager, err := NewManager(secondRuntime, &testHost{ingress: succeeding}, root, []Manifest{manifest})
	if err != nil {
		t.Fatal(err)
	}
	defer secondManager.Shutdown(ctx)
	if err = secondManager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		succeeding.mu.Lock()
		count := len(succeeding.messages)
		succeeding.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retried messages=%d", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDispatcherSeparatesInterleavedInboundAndDeliveryResults(t *testing.T) {
	ctx, root := context.Background(), findRepoRoot(t)
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ingress := &testIngress{handleEntered: make(chan struct{}), handleRelease: make(chan struct{})}
	host := &testHost{ingress: ingress}
	manager, err := NewManager(runtime, host, root, []Manifest{testManifest(t, "interleaved")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	if err = manager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ingress.handleEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("inbound event did not reach Core")
	}
	event, err := sessionevent.NewEvent("session-1", sessionevent.TypeMessage, sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartText, Text: "outbound"}}}, sessionevent.Correlation{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	delivery := sessionevent.Delivery{ID: "interleaved-delivery", Event: event, Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "interleaved"}}
	envelope, err := gateway.NewOutboundEnvelope(delivery)
	if err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	adapter := host.adapters["interleaved"]
	host.mu.Unlock()
	delivered := make(chan error, 1)
	go func() { delivered <- adapter.Deliver(ctx, envelope) }()
	select {
	case err = <-delivered:
		t.Fatalf("delivery bypassed blocked dispatcher: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(ingress.handleRelease)
	if err = <-delivered; err != nil {
		t.Fatal(err)
	}
	ingress.mu.Lock()
	messageCount := len(ingress.messages)
	ingress.mu.Unlock()
	if messageCount != 1 {
		t.Fatalf("inbound messages=%d", messageCount)
	}
}

func TestConcurrentLifecycleIsSerializedAndStatusRemainsRunning(t *testing.T) {
	ctx, root := context.Background(), findRepoRoot(t)
	runtime, _ := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	host := &testHost{ingress: &testIngress{}, registerEntered: make(chan struct{}), registerRelease: make(chan struct{})}
	manager, err := NewManager(runtime, host, root, []Manifest{testManifest(t, "concurrent")})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- manager.Enable(ctx, "concurrent") }()
	<-host.registerEntered
	go func() { results <- manager.Enable(ctx, "concurrent") }()
	close(host.registerRelease)
	if err = <-results; err != nil {
		t.Fatal(err)
	}
	if err = <-results; err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	registers := host.registers
	host.mu.Unlock()
	if registers != 1 {
		t.Fatalf("Register calls=%d", registers)
	}
	host.unregisterEntered = make(chan struct{})
	host.unregisterRelease = make(chan struct{})
	disabled := make(chan error, 1)
	go func() { disabled <- manager.Disable(ctx, "concurrent") }()
	<-host.unregisterEntered
	statuses := manager.Statuses()
	if len(statuses) != 1 || !statuses[0].Running {
		t.Fatalf("status during stop=%#v", statuses)
	}
	close(host.unregisterRelease)
	if err = <-disabled; err != nil {
		t.Fatal(err)
	}
	if manager.Statuses()[0].Running {
		t.Fatal("installation still running after disable")
	}
	if err = manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentShutdownWaitsForLifecycleCompletion(t *testing.T) {
	ctx, root := context.Background(), findRepoRoot(t)
	runtime, _ := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	host := &testHost{ingress: &testIngress{}}
	manager, err := NewManager(runtime, host, root, []Manifest{testManifest(t, "shutdown")})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.LoadEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	host.unregisterEntered = make(chan struct{})
	host.unregisterRelease = make(chan struct{})
	results := make(chan error, 2)
	go func() { results <- manager.Shutdown(ctx) }()
	<-host.unregisterEntered
	go func() { results <- manager.Shutdown(ctx) }()
	select {
	case err = <-results:
		t.Fatalf("shutdown returned before adapter stopped: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(host.unregisterRelease)
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnableCannotRegisterAfterShutdownBegins(t *testing.T) {
	ctx, root := context.Background(), findRepoRoot(t)
	runtime, _ := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	host := &testHost{ingress: &testIngress{}}
	manager, err := NewManager(runtime, host, root, []Manifest{testManifest(t, "activation-race")})
	if err != nil {
		t.Fatal(err)
	}
	entry := manager.entries["activation-race"]
	entry.opMu.Lock()
	enabled := make(chan error, 1)
	go func() { enabled <- manager.Enable(ctx, "activation-race") }()
	shutdown := make(chan error, 1)
	go func() { shutdown <- manager.Shutdown(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.mu.Lock()
		closing := manager.closing
		manager.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not establish activation boundary")
		}
		time.Sleep(time.Millisecond)
	}
	entry.opMu.Unlock()
	if err = <-enabled; !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Enable error=%v", err)
	}
	if err = <-shutdown; err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	registers := host.registers
	host.mu.Unlock()
	if registers != 0 {
		t.Fatalf("registrations after shutdown=%d", registers)
	}
}

func TestInstallStagesArtifactAndReconstructsManifest(t *testing.T) {
	ctx, dataRoot := context.Background(), t.TempDir()
	runtime, _ := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(dataRoot, "state")), t.TempDir())
	manager, err := NewManager(runtime, &testHost{ingress: &testIngress{}}, dataRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, "installed")
	manifest.Artifact.Path = filepath.Join(findRepoRoot(t), "Core/Extension/testdata/gateway.wasm")
	manifest.Enabled = false
	if err = manager.Install(manifest); err != nil {
		t.Fatal(err)
	}
	if err = manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dataRoot, "extensions", "installed", "installed", "module.wasm")
	if _, err = os.Stat(staged); err != nil {
		t.Fatal(err)
	}
	runtime2, _ := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(dataRoot, "state")), t.TempDir())
	manager2, err := NewManager(runtime2, &testHost{ingress: &testIngress{}}, dataRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager2.Shutdown(ctx)
	statuses := manager2.Statuses()
	if len(statuses) != 1 || statuses[0].ManagedBy != ManagedInstalled {
		t.Fatalf("reconstructed statuses=%#v", statuses)
	}
}

func TestConfiguredArtifactCannotEscapeDataRoot(t *testing.T) {
	ctx, dataRoot := context.Background(), t.TempDir()
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest := testManifest(t, "escape")
	manifest.Artifact.Path = "../gateway.wasm"
	if err = runtime.Load(ctx, manifest, dataRoot); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("path escape error=%v", err)
	}
}

func TestArtifactAndSecretSymlinkEscapesAreRejected(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	wasm, err := os.ReadFile(filepath.Join(findRepoRoot(t), "Core/Extension/testdata/gateway.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	outsideArtifact := filepath.Join(outside, "gateway.wasm")
	if err = os.WriteFile(outsideArtifact, wasm, 0600); err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	if err = os.Symlink(outsideArtifact, filepath.Join(dataRoot, "gateway.wasm")); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, "artifact-link")
	manifest.Artifact.Path = "gateway.wasm"
	if err = runtime.Load(ctx, manifest, dataRoot); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("artifact symlink escape error=%v", err)
	}
	_ = runtime.Close(ctx)

	dataRoot = t.TempDir()
	if err = os.WriteFile(filepath.Join(dataRoot, "gateway.wasm"), wasm, 0600); err != nil {
		t.Fatal(err)
	}
	secretOutside := filepath.Join(outside, "token")
	if err = os.WriteFile(secretOutside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	secretsRoot := t.TempDir()
	if err = os.Symlink(secretOutside, filepath.Join(secretsRoot, "token")); err != nil {
		t.Fatal(err)
	}
	runtime, err = NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), secretsRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	manifest = testManifest(t, "secret-link", CapabilitySecrets)
	manifest.Artifact.Path = "gateway.wasm"
	manifest.Secrets = map[string]string{"telegram_token": "token"}
	if err = runtime.Load(ctx, manifest, dataRoot); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("secret symlink escape error=%v", err)
	}
}

func TestSecretTraversalAndAbsolutePathsAreRejected(t *testing.T) {
	ctx, parent := context.Background(), t.TempDir()
	dataRoot, secretsRoot := filepath.Join(parent, "data"), filepath.Join(parent, "secrets")
	if err := os.MkdirAll(dataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(findRepoRoot(t), "Core/Extension/testdata/gateway.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "gateway.wasm"), wasm, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside-token")
	if err = os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, secretPath := range map[string]string{"traversal": "../outside-token", "absolute": outside} {
		t.Run(name, func(t *testing.T) {
			runtime, err := NewWASMRuntime(ctx, NewFileStateStore(filepath.Join(t.TempDir(), "state")), secretsRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close(ctx)
			manifest := testManifest(t, "secret-"+name, CapabilitySecrets)
			manifest.Artifact.Path = "gateway.wasm"
			manifest.Secrets = map[string]string{"telegram_token": secretPath}
			if err = runtime.Load(ctx, manifest, dataRoot); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("secret path error=%v", err)
			}
		})
	}
}

func TestAcceptedInboundIsNotReplayedAcrossPersistenceBoundaries(t *testing.T) {
	for _, pendingRetained := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending-retained-%t", pendingRetained), func(t *testing.T) {
			ctx, root, stateRoot := context.Background(), findRepoRoot(t), filepath.Join(t.TempDir(), "state")
			state := NewFileStateStore(stateRoot)
			if err := state.Put(ctx, "accepted", "gateway/inbound-accepted/1", []byte("accepted")); err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"event_version":1,"event_type":"message.incoming","source":"synthetic","update_id":1,"chat_id":7,"sender_id":9,"text":"hello"}`)
			message := GuestMessage{Version: 0, Type: "outgoing", Payload: payload}
			if pendingRetained {
				data, err := json.Marshal([]GuestMessage{message})
				if err != nil {
					t.Fatal(err)
				}
				if err = state.Put(ctx, "accepted", pendingInboundKey, data); err != nil {
					t.Fatal(err)
				}
			}
			runtime, err := NewWASMRuntime(ctx, state, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close(ctx)
			manifest := testManifest(t, "accepted")
			if err = runtime.Load(ctx, manifest, root); err != nil {
				t.Fatal(err)
			}
			ingress := &testIngress{}
			adapter := NewGatewayAdapter(manifest, runtime)
			adapter.ingress, adapter.routes = ingress, ingress
			if err = adapter.restorePending(ctx); err != nil {
				t.Fatal(err)
			}
			if pendingRetained {
				adapter.retryInbound(ctx)
			} else {
				adapter.dispatch(ctx, []GuestMessage{message}, nil)
			}
			ingress.mu.Lock()
			attempts := ingress.attempts
			ingress.mu.Unlock()
			if attempts != 0 {
				t.Fatalf("accepted event replayed into Core %d times", attempts)
			}
		})
	}
}

func TestTelegramABICompatibilityWhenArtifactAvailable(t *testing.T) {
	artifact := os.Getenv("NEONDOLL_TELEGRAM_WASM")
	if artifact == "" {
		t.Skip("set NEONDOLL_TELEGRAM_WASM to the independently built Telegram artifact")
	}
	wasm, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	dataRoot, secretsRoot := t.TempDir(), t.TempDir()
	copied := filepath.Join(dataRoot, "telegram.wasm")
	if err := os.WriteFile(copied, wasm, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsRoot, "token"), []byte("123:test"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, ExtensionID: "telegram", InstanceID: "telegram-compat", Contract: ContractGateway, ABI: 2, Artifact: Artifact{Path: "telegram.wasm", SHA256: fmt.Sprintf("%x", sum)}, Configuration: json.RawMessage(`{"version":1,"telegram":{"host":"https://api.telegram.org"},"polling":{"timeout_seconds":1,"allowed_updates":["message"]}}`), Requested: []Capability{CapabilityHTTP, CapabilitySecrets, CapabilityState, CapabilityLog}, Granted: []Capability{CapabilitySecrets, CapabilityState, CapabilityLog}, Secrets: map[string]string{"telegram_token": "token"}, Gateway: &GatewayConfiguration{Platform: "telegram", DollID: "doll", ConversationPrefix: "telegram/chat/"}}
	runtime, err := NewWASMRuntime(context.Background(), NewFileStateStore(filepath.Join(t.TempDir(), "state")), secretsRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	if err := runtime.Load(context.Background(), manifest, dataRoot); err != nil {
		t.Fatalf("Telegram ABI v2 load: %v", err)
	}
	if err := runtime.HandleEvent("telegram-compat", "session.route", []byte(`{"chat_id":7,"delivery_id":"compat-delivery"}`)); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"delivery_id":"compat-delivery","event":{"type":"message","payload":{"parts":[{"type":"text","text":"hello"}]}}}`)
	if err := runtime.HandleEvent("telegram-compat", "session.event", payload); err != nil {
		t.Fatal(err)
	}
	messages := runtime.TakeOutbound("telegram-compat")
	found := false
	for _, message := range messages {
		var result deliveryResult
		if json.Unmarshal(message.Payload, &result) == nil && result.EventType == "delivery.result" {
			found = true
			if result.DeliveryID != "compat-delivery" {
				t.Fatalf("Telegram delivery correlation=%q", result.DeliveryID)
			}
		}
	}
	if !found {
		t.Fatal("Telegram guest emitted no delivery.result")
	}
	if err := runtime.HandleEvent("telegram-compat", "inbound.accepted", []byte(`{"event_id":"42"}`)); err != nil {
		t.Fatal(err)
	}
	if messages = runtime.TakeOutbound("telegram-compat"); len(messages) != 0 {
		t.Fatalf("current Telegram guest unexpectedly handled inbound.accepted: %#v", messages)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
