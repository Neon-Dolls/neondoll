package extension

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	mu       sync.Mutex
	messages []gateway.VerifiedSender
}

func (*testIngress) ResolvePlatformSession(context.Context, string) (session.ID, error) {
	return "session-1", nil
}
func (*testIngress) ExternalConversationRef(context.Context, session.ID) (string, error) {
	return "synthetic/chat/7", nil
}
func (i *testIngress) HandleLegacy(_ context.Context, _ session.ID, s gateway.VerifiedSender, _ *events.Event) (*events.Event, error) {
	i.mu.Lock()
	i.messages = append(i.messages, s)
	i.mu.Unlock()
	return nil, nil
}
func (i *testIngress) HandlePlatformMessage(ctx context.Context, _ string, s gateway.VerifiedSender, e *events.Event) (*gateway.PlatformResponse, error) {
	_, err := i.HandleLegacy(ctx, "session-1", s, e)
	return &gateway.PlatformResponse{SessionID: "session-1"}, err
}
func (*testIngress) HandleSessionEvent(context.Context, gateway.VerifiedSender, sessionevent.Event) (*permissionrequest.Request, error) {
	return nil, nil
}

type testHost struct {
	mu       sync.Mutex
	ingress  *testIngress
	adapters map[string]gateway.Adapter
}

func (h *testHost) Register(ctx context.Context, a gateway.Adapter) error {
	h.mu.Lock()
	if h.adapters == nil {
		h.adapters = map[string]gateway.Adapter{}
	}
	if h.adapters[string(a.InstanceID())] != nil {
		h.mu.Unlock()
		return gateway.ErrAlreadyRegistered
	}
	h.adapters[string(a.InstanceID())] = a
	h.mu.Unlock()
	return a.Start(ctx, h.ingress)
}
func (h *testHost) Unregister(ctx context.Context, id externalidentity.GatewayInstanceID) error {
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
