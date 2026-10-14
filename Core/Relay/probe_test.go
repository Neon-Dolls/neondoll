// SPDX-License-Identifier: AGPL-3.0-only
package relay

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

const testProbeCredential = "test-token"

// testProbeRegistry sets up a minimal registry with one registration, one
// route, and a route credential for testing.
func testProbeRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry(1, 1)
	if err := reg.AddRegistration("test-reg"); err != nil {
		t.Fatalf("AddRegistration: %v", err)
	}
	if _, err := reg.AllocateRoute("test-reg", RouteID(1)); err != nil {
		t.Fatalf("AllocateRoute: %v", err)
	}
	if err := reg.SetRouteCredentials("test-reg", RouteID(1), RouteCredentials{Token: testProbeCredential}); err != nil {
		t.Fatalf("SetRouteCredentials: %v", err)
	}
	return reg
}

func TestProbeListenerStartStop(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := pl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pl.Addr() == nil {
		t.Fatal("Addr() is nil after Start")
	}

	pl.Stop()
	// Double-stop should not panic or hang.
	pl.Stop()
}

func TestProbeListenerAddrBeforeStart(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if pl.Addr() != nil {
		t.Fatal("Addr() should be nil before Start")
	}
}

func TestProbeListenerStopBeforeStart(t *testing.T) {
	// Regression: Stop() must not block when Start was never called.
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	pl.Stop()
}

func TestProbeListenerRoundTrip(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := pl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pl.Stop()

	addr := pl.Addr()
	if addr == nil {
		t.Fatal("Addr() is nil")
	}

	nonce, err := GenerateProbeNonce()
	if err != nil {
		t.Fatalf("GenerateProbeNonce: %v", err)
	}

	req := ProbeRequest(RouteID(1), nonce, testProbeCredential)
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(req); err != nil {
		t.Fatalf("Write: %v", err)
	}

	resp := make([]byte, ProbeDatagramSize+64)
	n, err := conn.Read(resp)
	if err != nil {
		t.Fatalf("Read response: %v", err)
	}
	resp = resp[:n]

	if err := VerifyProbeResponse(resp, RouteID(1), nonce, testProbeCredential); err != nil {
		t.Fatalf("VerifyProbeResponse: %v", err)
	}
}

func TestProbeListenerBadToken(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := pl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pl.Stop()

	nonce, err := GenerateProbeNonce()
	if err != nil {
		t.Fatalf("GenerateProbeNonce: %v", err)
	}

	// Use a wrong token — the listener should not respond.
	req := ProbeRequest(RouteID(1), nonce, "wrong-token")
	conn, err := net.DialUDP("udp", nil, pl.Addr())
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Set a read deadline so we don't hang if no response arrives.
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	resp := make([]byte, ProbeDatagramSize+64)
	_, err = conn.Read(resp)
	if err == nil {
		t.Fatal("expected timeout/error for bad token, got response")
	}
}

func TestProbeListenerUnknownRoute(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := pl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pl.Stop()

	nonce, err := GenerateProbeNonce()
	if err != nil {
		t.Fatalf("GenerateProbeNonce: %v", err)
	}

	// RouteID(2) doesn't exist — no response.
	req := ProbeRequest(RouteID(2), nonce, testProbeCredential)
	conn, err := net.DialUDP("udp", nil, pl.Addr())
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	resp := make([]byte, ProbeDatagramSize+64)
	_, err = conn.Read(resp)
	if err == nil {
		t.Fatal("expected timeout/error for unknown route, got response")
	}
}

func TestProbeListenerSizeMismatch(t *testing.T) {
	reg := testProbeRegistry(t)
	pl := NewProbeListener(ProbeListenerConfig{Enabled: true, Port: 0}, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := pl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pl.Stop()

	conn, err := net.DialUDP("udp", nil, pl.Addr())
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	// Send a datagram that's too short.
	if _, err := conn.Write([]byte("tooshort")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	resp := make([]byte, ProbeDatagramSize+64)
	_, err = conn.Read(resp)
	if err == nil {
		t.Fatal("expected timeout/error for size mismatch, got response")
	}
}