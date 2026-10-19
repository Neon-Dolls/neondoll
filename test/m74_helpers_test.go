//go:build e2e

package integration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/Neon-Dolls/neondoll/Body"
	corebody "github.com/Neon-Dolls/neondoll/Core/Body"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

// ─────────────────────────────────────────────────────────────────────
// parseHandshakeTimestamp — extract last_handshake_time_nsec from
// WireGuard IpcGet output and return the corresponding time.Time.
// ─────────────────────────────────────────────────────────────────────

func parseHandshakeTimestamp(dump string) (time.Time, error) {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "last_handshake_time_nsec=") {
			continue
		}
		raw := strings.TrimPrefix(line, "last_handshake_time_nsec=")
		nsec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse last_handshake_time_nsec %q: %w", raw, err)
		}
		if nsec == 0 {
			return time.Time{}, nil
		}
		sec := nsec / 1_000_000_000
		nsecRem := nsec % 1_000_000_000
		return time.Unix(sec, nsecRem), nil
	}
	return time.Time{}, fmt.Errorf("last_handshake_time_nsec not found in IpcGet output")
}

// ── proveDirectOverlayEcho ───────────────────────────────────────────

func proveDirectOverlayEcho(t *testing.T, ctx context.Context, listenStack, dialStack *netstack.Net, targetAddr netip.Addr, label string) {
	t.Helper()
	prefix := fmt.Sprintf("[%s]", label)

	// Listen on the provider-side netstack
	ln, err := listenStack.ListenTCP(&net.TCPAddr{IP: targetAddr.AsSlice(), Port: 7777})
	if err != nil {
		t.Fatalf("%s listen on %s: %v", prefix, targetAddr, err)
	}
	defer ln.Close()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			t.Logf("%s accept error: %v", prefix, aErr)
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		_, _ = conn.Write(buf[:n])
	}()

	// Dial from the caller-side netstack
	conn, err := dialStack.Dial("tcp", net.JoinHostPort(targetAddr.String(), "7777"))
	if err != nil {
		t.Fatalf("%s dial %s:7777: %v", prefix, targetAddr, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("%s write: %v", prefix, err)
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("%s read: %v", prefix, err)
	}
	got := string(buf[:n])
	if got != "ping" {
		t.Fatalf("%s echo mismatch: got %q, want %q", prefix, got, "ping")
	}
	t.Logf("%s overlay TCP echo OK", prefix)
}

// ── updateBodyCoreEndpoint ─────────────────────────────────────────────
//
// updateBodyCoreEndpoint updates the body tunnel's peer endpoint to
// point at the new core's stable listen port after reconstruction.
func updateBodyCoreEndpoint(t *testing.T, bodyTun *body.BodyTunnel, corePubKey []byte, port int) {
	t.Helper()
	pubHex := hex.EncodeToString(corePubKey)
	uapiCmd := fmt.Sprintf("public_key=%s\nendpoint=127.0.0.1:%d\n", pubHex, port)
	if err := bodyTun.IpcSet(uapiCmd); err != nil {
		t.Fatalf("update body core endpoint: %v", err)
	}
	t.Logf("Body tunnel peer endpoint updated to 127.0.0.1:%d", port)
}

// ── proveWSOverlay ───────────────────────────────────────────────────
//
// proveWSOverlay starts a DollLink WebSocket server on listenStack and
// connects from dialStack, performs WS upgrade, then sends a text frame
// and verifies echo response.
//
// It reuses wsLogger (implementing ws.Logger) and echoHandler from
// m3_e2e_test.go (same package, same build tag).

func proveWSOverlay(t *testing.T, ctx context.Context, listenStack, dialStack *netstack.Net, targetAddr netip.Addr, log *slog.Logger) {
	t.Helper()
	t.Log("proveWSOverlay: WebSocket round-trip over overlay ...")

	// Start WS server on listenStack
	srv := ws.New(ws.Config{}, &wsLogger{inner: log}, echoHandler{})
	wsLn, err := listenStack.ListenTCP(&net.TCPAddr{IP: targetAddr.AsSlice(), Port: 8080})
	if err != nil {
		t.Fatalf("ws listen on %s:8080: %v", targetAddr, err)
	}
	defer wsLn.Close()

	srv.SetListener(wsLn)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Start(ctx)
	}()
	defer func() {
		_ = srv.Shutdown(ctx)
	}()

	// Dial from body netstack, do a manual WS upgrade (same approach as m3)
	raw, err := dialStack.Dial("tcp", net.JoinHostPort(targetAddr.String(), "8080"))
	if err != nil {
		t.Fatalf("ws dial %s:8080: %v", targetAddr, err)
	}
	defer raw.Close()

	upgradeReq := "GET /ws HTTP/1.1\r\n" +
		"Host: " + targetAddr.String() + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"

	if _, err := raw.Write([]byte(upgradeReq)); err != nil {
		t.Fatalf("ws upgrade write: %v", err)
	}

	respBuf := make([]byte, 1024)
	n, err := raw.Read(respBuf)
	if err != nil {
		t.Fatalf("ws upgrade read: %v", err)
	}
	resp := string(respBuf[:n])
	if !strings.Contains(resp, "101 Switching Protocols") {
		t.Fatalf("ws upgrade response: got %q, want 101", resp)
	}
	t.Logf("WS upgrade OK (101 Switching Protocols)")

	// Send a real events.Event over the WebSocket (same shape as m3)
	event := events.Event{
		ID:        "m74-e2e-001",
		Type:      events.TypeMessage,
		Source:    "test-body",
		Timestamp: time.Now().UTC(),
		Payload: events.MessagePayload{
			Text: "ping",
		},
	}
	eventData, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("ws marshal event: %v", err)
	}
	frame := buildWSTextFrame(eventData)
	if _, err := raw.Write(frame); err != nil {
		t.Fatalf("ws text frame write: %v", err)
	}

	// Read echo frame back
	frameBuf := make([]byte, 4096)
	n, err = raw.Read(frameBuf)
	if err != nil {
		t.Fatalf("ws echo frame read: %v", err)
	}
	echoPayload, err := parseWSFrame(frameBuf[:n])
	if err != nil {
		t.Fatalf("ws parse echo frame: %v", err)
	}
	var respEvent events.Event
	if err := json.Unmarshal(echoPayload, &respEvent); err != nil {
		t.Fatalf("ws unmarshal echo event: %v (raw: %s)", err, string(echoPayload))
	}
	wantText := "response_ping"
	gotText := ""
	if p, ok := respEvent.Payload.(map[string]any); ok {
		if t, ok := p["text"].(string); ok {
			gotText = t
		}
	}
	if gotText != wantText {
		t.Fatalf("ws echo payload mismatch: got %q, want %q (correlation %q)",
			gotText, wantText, respEvent.CorrelationID)
	}

	t.Log("proveWSOverlay: WS round-trip OK (ping -> response_ping)")
}

// ── proveCapabilityExecution ─────────────────────────────────────────
//
// proveCapabilityExecution proves the core capability system works
// after reconstruction by executing runtime.info:read through the
// guard for the local body.

func proveCapabilityExecution(t *testing.T) {
	t.Helper()
	t.Log("proveCapabilityExecution: proving guard.Execute(local, runtime.info:read) ...")

	registry := corebody.NewRegistry()

	// The local body already has "runtime.info" registered. We just need
	// an authority rule that allows the local body to execute it.
	guard := corebody.NewGuard(
		registry,
		corebody.NewRuleEvaluator(
			corebody.AuthorityRule{
				Doll:       "m74-test-doll",
				Body:       corebody.LocalBodyID,
				Capability: "runtime.info",
				Operation:  "read",
				Decision:   corebody.AuthorityAllow,
				Reason:     "allow for m7.4 test",
			},
		),
	)

	res := guard.Execute(corebody.ExecutionRequest{
		ExecutionID: "m74-e2e-prove-001",
		Doll:        "m74-test-doll",
		Body:        corebody.LocalBodyID,
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != corebody.StatusSuccess {
		t.Fatalf("guard.Execute(runtime.info:read) = status %q, want %q", res.Status, corebody.StatusSuccess)
	}
	t.Logf("proveCapabilityExecution: OK — status=%q", res.Status)
}

// ── WebSocket frame helpers (from m3_e2e_test.go) ────────────────────
//
// buildWSTextFrame and parseWSFrame are defined in m3_e2e_test.go
// in the same package integration — they are available here.


