//go:build e2e

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

// TestM47_IsolationE2E proves live route isolation using a real
// ControlServer + Service with two independent ControlClients.
//
// Proofs:
//  1. Two registrations exist (ActiveRegistrations == 2).
//  2. Each route gets its own UDP endpoint (distinct, not shared).
//  3. Closing a route on client A does not affect route B's endpoint.
//  4. Closing route 1 removes it; route 2 still exists in registry.
func TestM47_IsolationE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	relayPortSvc := pickPort(t)
	relayPortWS := pickPort(t)

	relayCfg := relay.DefaultServiceConfig()
	relayCfg.UDP.ListenAddress = "127.0.0.1"
	relayCfg.UDP.PortMin = relayPortSvc
	relayCfg.UDP.PortMax = relayPortSvc + 1 // two-port range so each route gets a distinct endpoint
	svc, err := relay.NewService(relayCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	registry := svc.Registry()

	sCtx, sCancel := context.WithCancel(ctx)
	defer sCancel()
	go func() { _ = svc.Start(sCtx) }()

	cs := relay.NewControlServer(svc, fmt.Sprintf("127.0.0.1:%d", relayPortWS), log)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	go func() { _ = cs.Start(csCtx) }()
	time.Sleep(200 * time.Millisecond)

	relayURL := fmt.Sprintf("ws://127.0.0.1:%d/relay", relayPortWS)

	// ── Client A: registration A, route 1 ──
	caCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "tkn-a",
	}
	ctrlA := relay.NewControlClient(caCfg)
	if err := ctrlA.Start(ctx); err != nil {
		t.Fatalf("ctrlA Start: %v", err)
	}
	defer ctrlA.Shutdown()

	openedA, err := ctrlA.OpenRoute(ctx, 1, relay.RouteCredentials{Token: "r1-creds"})
	if err != nil {
		t.Fatalf("OpenRoute 1 (A): %v", err)
	}

	// ── Client B: registration B, route 2 ──
	cbCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "tkn-b",
	}
	ctrlB := relay.NewControlClient(cbCfg)
	if err := ctrlB.Start(ctx); err != nil {
		t.Fatalf("ctrlB Start: %v", err)
	}
	defer ctrlB.Shutdown()

	openedB, err := ctrlB.OpenRoute(ctx, 2, relay.RouteCredentials{Token: "r2-creds"})
	if err != nil {
		t.Fatalf("OpenRoute 2 (B): %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	// ── Proof 1: two registrations ──
	if regs := cs.ActiveRegistrations(); regs != 2 {
		t.Fatalf("expected 2 registrations, got %d", regs)
	}
	t.Logf("Proof 1: %d registrations", cs.ActiveRegistrations())

	// ── Proof 2: distinct UDP endpoints ──
	epA := openedA.AllocatedEndpoint
	epB := openedB.AllocatedEndpoint
	if epA == "" || epB == "" {
		t.Fatal("route endpoints must be non-empty")
	}
	if epA == epB {
		t.Fatalf("routes share endpoint %q — isolation broken", epA)
	}
	t.Logf("Proof 2: endpoint A=%q  B=%q (distinct)", epA, epB)

	// ── Proof 3: close route 1, route 2 still exists ──
	if err := ctrlA.CloseRoute(ctx, 1); err != nil {
		t.Fatalf("CloseRoute A: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if _, got := registry.Route(relay.RouteID(1)); got {
		t.Error("route 1 leaked after CloseRoute")
	}
	r2Entry, got := registry.Route(relay.RouteID(2))
	if !got {
		t.Fatal("route 2 missing after close of route 1")
	}
	if r2Entry.Endpoint != epB {
		t.Errorf("route 2 endpoint changed after close of 1: was %s, now %s", epB, r2Entry.Endpoint)
	}
	t.Logf("Proof 3: route 2 endpoint=%q unchanged after route 1 closure", r2Entry.Endpoint)

	// ── Proof 4: close A's Shutdown tears down A but B stays up ──
	ctrlA.Shutdown()
	time.Sleep(200 * time.Millisecond)

	r2Entry2, got := registry.Route(relay.RouteID(2))
	if !got {
		t.Fatal("route 2 disappeared after client A shutdown")
	}
	if r2Entry2.Endpoint != epB {
		t.Errorf("route 2 endpoint changed after A shutdown: %s", r2Entry2.Endpoint)
	}
	t.Logf("Proof 4: route 2 endpoint=%q survives client A shutdown", r2Entry2.Endpoint)

	t.Log("=== M4.7 Isolation E2E: ALL PASS ===")
}
