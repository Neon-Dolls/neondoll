package body

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

type registrationMemberships struct {
	mu       sync.RWMutex
	statuses map[string]network.MembershipStatus
}

func (m *registrationMemberships) ListMemberships(context.Context) ([]*network.Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*network.Membership, 0, len(m.statuses))
	for bodyID, status := range m.statuses {
		result = append(result, &network.Membership{BodyID: bodyID, Status: status})
	}
	return result, nil
}

func (m *registrationMemberships) set(bodyID string, status network.MembershipStatus) {
	m.mu.Lock()
	m.statuses[bodyID] = status
	m.mu.Unlock()
}

type registrationTransport struct{}

func (registrationTransport) Execute(context.Context, string, string, execution.RequestPayload) (execution.Result, error) {
	return execution.Result{}, nil
}

func registrationCapability(id string) capabilities.Capability {
	return capabilities.Capability{ID: id, Operations: []string{"run"}, Available: true}
}

func TestRemoteRegistrarMembershipAndStaleSessionLifecycle(t *testing.T) {
	members := &registrationMemberships{statuses: map[string]network.MembershipStatus{
		"body-active":  network.MembershipActive,
		"body-revoked": network.MembershipRevoked,
	}}
	registry := NewRegistry()
	registrar, err := NewRemoteRegistrar(registry, members, registrationTransport{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := registrar.SessionConnected(ctx, "unknown", "body-unknown"); !errors.Is(err, ErrRemoteMembershipUnknown) {
		t.Fatalf("unknown membership error = %v", err)
	}
	if err := registrar.SessionConnected(ctx, "revoked", "body-revoked"); !errors.Is(err, ErrRemoteMembershipRevoked) {
		t.Fatalf("revoked membership error = %v", err)
	}

	if err := registrar.SessionConnected(ctx, "session-old", "body-active"); err != nil {
		t.Fatal(err)
	}
	if err := registrar.CapabilitiesAdvertised(ctx, "session-old", "body-active", []capabilities.Capability{registrationCapability("old")}); err != nil {
		t.Fatal(err)
	}
	if err := registrar.SessionConnected(ctx, "session-new", "body-active"); err != nil {
		t.Fatal(err)
	}
	if registry.Count() != 2 {
		t.Fatalf("repeated registration created duplicates: count=%d", registry.Count())
	}
	if err := registry.ResolveCapability("body-active", "old", "run"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replacement did not make old advertisement unavailable: %v", err)
	}
	if err := registrar.CapabilitiesAdvertised(ctx, "session-old", "body-active", []capabilities.Capability{registrationCapability("stale")}); !errors.Is(err, ErrStaleRemoteSession) {
		t.Fatalf("stale advertisement error = %v", err)
	}
	if err := registrar.CapabilitiesAdvertised(ctx, "session-new", "body-active", []capabilities.Capability{registrationCapability("new")}); err != nil {
		t.Fatal(err)
	}
	registrar.SessionDisconnected("session-old", "body-active")
	if err := registry.ResolveCapability("body-active", "new", "run"); err != nil {
		t.Fatalf("stale disconnect changed current session: %v", err)
	}
	registrar.SessionDisconnected("session-new", "body-active")
	if err := registry.ResolveCapability("body-active", "new", "run"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("current disconnect availability = %v", err)
	}

	// A reconstructed Core creates a fresh runtime Registry, then accepts the
	// same durable Body identity and its fresh advertisement without re-pairing.
	reconstructedRegistry := NewRegistry()
	reconstructed, err := NewRemoteRegistrar(reconstructedRegistry, members, registrationTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.SessionConnected(ctx, "session-after-restart", "body-active"); err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.CapabilitiesAdvertised(ctx, "session-after-restart", "body-active", []capabilities.Capability{registrationCapability("restored")}); err != nil {
		t.Fatal(err)
	}
	if err := reconstructedRegistry.ResolveCapability("body-active", "restored", "run"); err != nil {
		t.Fatalf("reconstructed registration = %v", err)
	}
}

func TestRemoteRegistrarConcurrentRepeatedRegistration(t *testing.T) {
	members := &registrationMemberships{statuses: map[string]network.MembershipStatus{"body-active": network.MembershipActive}}
	registry := NewRegistry()
	registrar, err := NewRemoteRegistrar(registry, members, registrationTransport{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- registrar.SessionConnected(context.Background(), fmt.Sprintf("session-%d", i), "body-active")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if registry.Count() != 2 {
		t.Fatalf("concurrent registration count = %d, want Local plus one remote", registry.Count())
	}
}

func TestRemoteRegistrarReconstructsFromPersistedMembership(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	networkStore := store.(network.NetworkStore)
	nw, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatal(err)
	}
	membership, err := nw.NewMembership("body-persisted")
	if err != nil {
		t.Fatal(err)
	}
	if err := nw.ActivateMembership(membership.PeerID); err != nil {
		t.Fatal(err)
	}
	if err := networkStore.SaveNetwork(ctx, nw); err != nil {
		t.Fatal(err)
	}
	if err := networkStore.SaveMembership(ctx, nw.GetMembership(membership.PeerID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reconstructedStore := reopened.(network.NetworkStore)
	reconstructedNetwork, err := reconstructedStore.LoadNetwork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconstructedNetwork == nil || reconstructedNetwork.NetworkID != nw.NetworkID {
		t.Fatal("durable network identity was not reconstructed")
	}
	registry := NewRegistry()
	registrar, err := NewRemoteRegistrar(registry, reconstructedStore, registrationTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if err := registrar.SessionConnected(ctx, "reconstructed-session", "body-persisted"); err != nil {
		t.Fatal(err)
	}
	if err := registrar.CapabilitiesAdvertised(ctx, "reconstructed-session", "body-persisted", []capabilities.Capability{registrationCapability("restored")}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ResolveCapability("body-persisted", "restored", "run"); err != nil {
		t.Fatalf("persisted Body did not re-register after reconstruction: %v", err)
	}
}

const registrationAuthHeader = "X-Neondoll-Registration-Fixture"

type registrationHarness struct {
	t         *testing.T
	server    *ws.Server
	url       string
	registry  *Registry
	registrar *RemoteRegistrar
	members   *registrationMemberships
	clients   map[string]*ws.Client
	done      map[string]chan error
}

func newRegistrationHarness(t *testing.T, withAuthentication bool) *registrationHarness {
	t.Helper()
	members := &registrationMemberships{statuses: map[string]network.MembershipStatus{
		"body-a":       network.MembershipActive,
		"body-b":       network.MembershipActive,
		"body-c":       network.MembershipActive,
		"body-revoked": network.MembershipRevoked,
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := ws.Config{}
	if withAuthentication {
		cfg.AuthenticateExecution = func(request *http.Request) (string, error) {
			bodyID := request.Header.Get(registrationAuthHeader)
			if bodyID == "" {
				return "", errors.New("trusted fixture rejected credential")
			}
			return bodyID, nil
		}
	}
	server := ws.New(cfg, remoteTestLogger{}, nil)
	server.SetListener(listener)
	registry := NewRegistry()
	registrar, err := NewRemoteRegistrar(registry, members, server)
	if err != nil {
		t.Fatal(err)
	}
	server.SetExecutionSessionLifecycle(registrar)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(context.Background()) }()
	h := &registrationHarness{
		t: t, server: server, url: "ws://" + listener.Addr().String() + "/ws", registry: registry,
		registrar: registrar, members: members, clients: make(map[string]*ws.Client), done: make(map[string]chan error),
	}
	t.Cleanup(func() {
		for bodyID, client := range h.clients {
			_ = client.Close()
			select {
			case <-h.done[bodyID]:
			case <-time.After(time.Second):
				t.Errorf("Body %s client did not stop", bodyID)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("server shutdown: %v", err)
		}
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	return h
}

func (h *registrationHarness) connect(bodyID string, advertised []capabilities.Capability) {
	h.t.Helper()
	header := http.Header{registrationAuthHeader: []string{bodyID}}
	conn, _, err := websocket.DefaultDialer.Dial(h.url, header)
	if err != nil {
		h.t.Fatal(err)
	}
	dispatcher, err := execution.NewDispatcher(bodyID, execution.LocalPermissionFunc(
		func(context.Context, execution.Request) bool { return true },
	), execution.CapabilityExecutorFunc(
		func(_ context.Context, capability, _ string, _ map[string]any) (map[string]any, error) {
			return map[string]any{"body": bodyID, "capability": capability}, nil
		},
	))
	if err != nil {
		h.t.Fatal(err)
	}
	client, err := ws.NewClient(conn, ws.ClientConfig{AuthenticatedExecutionHandler: dispatcher}, remoteTestLogger{})
	if err != nil {
		h.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	h.clients[bodyID] = client
	h.done[bodyID] = done
	if err := client.AdvertiseCapabilities(context.Background(), bodyID, advertised); err != nil {
		h.t.Fatal(err)
	}
	h.waitCapability(bodyID, advertised)
}

func (h *registrationHarness) waitCapability(bodyID string, advertised []capabilities.Capability) {
	h.t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := h.registry.GetCapabilities(BodyID(bodyID))
		if err == nil && len(got) == len(advertised) {
			matches := true
			for i := range got {
				matches = matches && got[i].ID == advertised[i].ID && got[i].Available == advertised[i].Available
			}
			if matches {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("capabilities for %s were not registered", bodyID)
}

func (h *registrationHarness) disconnect(bodyID string) {
	h.t.Helper()
	client := h.clients[bodyID]
	_ = client.Close()
	select {
	case <-h.done[bodyID]:
	case <-time.After(time.Second):
		h.t.Fatalf("Body %s did not disconnect", bodyID)
	}
	delete(h.clients, bodyID)
	delete(h.done, bodyID)
}

func (h *registrationHarness) waitConnections(want int) {
	h.t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.server.ConnCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("connection count = %d, want %d", h.server.ConnCount(), want)
}

func TestProductionRemoteRegistrationThreeBodiesAndGuardRouting(t *testing.T) {
	h := newRegistrationHarness(t, true)
	capabilitiesByBody := map[string][]capabilities.Capability{
		"body-a": {registrationCapability("camera")},
		"body-b": {registrationCapability("filesystem")},
		"body-c": {registrationCapability("notification")},
	}
	for _, bodyID := range []string{"body-a", "body-b", "body-c"} {
		h.connect(bodyID, capabilitiesByBody[bodyID])
	}
	if h.registry.Count() != 4 {
		t.Fatalf("registry count = %d, want Local plus three remote Bodies", h.registry.Count())
	}
	rules := make([]AuthorityRule, 0, 3)
	for bodyID, advertised := range capabilitiesByBody {
		rules = append(rules, AuthorityRule{
			Doll: "doll", Body: BodyID(bodyID), Capability: advertised[0].ID, Operation: "run", Decision: AuthorityAllow,
		})
	}
	guard := NewGuard(h.registry, NewRuleEvaluator(rules...))
	type outcome struct {
		bodyID string
		result ExecutionResult
	}
	outcomes := make(chan outcome, len(capabilitiesByBody))
	for bodyID, advertised := range capabilitiesByBody {
		go func(bodyID, capabilityID string) {
			outcomes <- outcome{bodyID: bodyID, result: guard.Execute(ExecutionRequest{
				ExecutionID: "exec-" + bodyID, Doll: "doll", Body: BodyID(bodyID),
				Capability: capabilityID, Operation: "run",
			})}
		}(bodyID, advertised[0].ID)
	}
	for range capabilitiesByBody {
		outcome := <-outcomes
		if outcome.result.Status != StatusSuccess || outcome.result.ExecutionID != "exec-"+outcome.bodyID {
			t.Fatalf("Body %s result = %#v", outcome.bodyID, outcome.result)
		}
	}

	updated := []capabilities.Capability{registrationCapability("microphone")}
	if err := h.clients["body-a"].AdvertiseCapabilities(context.Background(), "body-b", []capabilities.Capability{registrationCapability("forged")}); err != nil {
		t.Fatal(err)
	}
	if err := h.clients["body-a"].AdvertiseCapabilities(context.Background(), "body-a", updated); err != nil {
		t.Fatal(err)
	}
	h.waitCapability("body-a", updated)
	if err := h.registry.ResolveCapability("body-a", "camera", "run"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("replaced capability remained registered: %v", err)
	}
	if err := h.registry.ResolveCapability("body-b", "filesystem", "run"); err != nil {
		t.Fatalf("Body A update affected Body B: %v", err)
	}
	if err := h.registry.ResolveCapability("body-b", "forged", "run"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("cross-Body advertisement mutated Body B: %v", err)
	}

	h.disconnect("body-b")
	deadline := time.Now().Add(time.Second)
	for {
		err := h.registry.ResolveCapability("body-b", "filesystem", "run")
		if errors.Is(err, ErrUnavailable) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected capability = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	h.connect("body-b", capabilitiesByBody["body-b"])
	if err := h.registry.ResolveCapability("body-b", "filesystem", "run"); err != nil {
		t.Fatalf("reconnected capability = %v", err)
	}
	local, ok := h.registry.Local()
	if !ok || local.ID() != LocalBodyID || local.Kind() != BodyKindLocal {
		t.Fatalf("Local Body changed: %#v, %v", local, ok)
	}
}

func TestProductionRemoteRegistrationRejectsMembershipAndNoAuthentication(t *testing.T) {
	authenticated := newRegistrationHarness(t, true)
	for _, bodyID := range []string{"body-unknown", "body-revoked"} {
		header := http.Header{registrationAuthHeader: []string{bodyID}}
		conn, _, err := websocket.DefaultDialer.Dial(authenticated.url, header)
		if err != nil {
			t.Fatal(err)
		}
		authenticated.waitConnections(1)
		if err := conn.WriteJSON(capabilities.NewAdvertisement(bodyID, []capabilities.Capability{registrationCapability("forged")})); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		authenticated.waitConnections(0)
	}
	if authenticated.registry.Count() != 1 {
		t.Fatalf("invalid membership registered: count=%d", authenticated.registry.Count())
	}

	unauthenticated := newRegistrationHarness(t, false)
	conn, _, err := websocket.DefaultDialer.Dial(unauthenticated.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unauthenticated.waitConnections(1)
	if err := conn.WriteJSON(capabilities.NewAdvertisement("body-a", []capabilities.Capability{registrationCapability("forged")})); err != nil {
		t.Fatal(err)
	}
	if unauthenticated.registry.Count() != 1 {
		t.Fatalf("unauthenticated Body registered: count=%d", unauthenticated.registry.Count())
	}
	_, err = unauthenticated.server.Execute(context.Background(), "body-a", "doll", execution.RequestPayload{
		ExecutionID: "blocked", Capability: "forged", Operation: "run", Arguments: map[string]any{},
	})
	if !errors.Is(err, ws.ErrNoAuthenticatedSession) {
		t.Fatalf("unauthenticated execution error = %v", err)
	}
}
