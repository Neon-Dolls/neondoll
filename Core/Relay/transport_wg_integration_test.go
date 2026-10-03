// Package relay — M4.5 real WireGuard runtime integration proof.
//
// This test proves that the real userspace WireGuard runtime (the same
// golang.zx2c4.com/wireguard device used in production) can consume AND
// emit encrypted WG datagrams through the Relay-backed conn.Bind. Two real
// WG devices are wired to a pair of RelayTransports connected by a fake
// Relay that forwards opaque binary frames between them; the devices then
// complete a real WG handshake and exchange real encrypted traffic.
package relay

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ── fake paired Relay ────────────────────────────────────────────────────────

// pairState coordinates two ControlClient connections so the fake Relay can
// forward binary frames between them (opaque passthrough, like a real Relay
// routing datagrams between two Cores).
type pairState struct {
	mu    sync.Mutex
	conns []*websocket.Conn

	// frameMu guards relay-side observation of forwarded frames.
	frameMu sync.Mutex
	frames  []*Frame
}

func (ps *pairState) record(f *Frame) {
	ps.frameMu.Lock()
	defer ps.frameMu.Unlock()
	ps.frames = append(ps.frames, f)
}

// forwardedFrames returns a copy of the frames the Relay forwarded.
func (ps *pairState) forwardedFrames() []*Frame {
	ps.frameMu.Lock()
	defer ps.frameMu.Unlock()
	out := make([]*Frame, len(ps.frames))
	copy(out, ps.frames)
	return out
}

// peerCount returns the number of registered peer connections.
func (ps *pairState) peerCount() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.conns)
}

// findPeer selects the first live connection that is not ours.
// Returns nil when only one connection is registered.
func (ps *pairState) findPeer(conn *websocket.Conn) *websocket.Conn {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, c := range ps.conns {
		if c != conn {
			return c
		}
	}
	return nil
}

// removeConn removes the given connection from the registered set.
// Called by each handler's defer when the WebSocket drops.
func (ps *pairState) removeConn(conn *websocket.Conn) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	cleaned := make([]*websocket.Conn, 0, len(ps.conns))
	for _, c := range ps.conns {
		if c != conn {
			cleaned = append(cleaned, c)
		}
	}
	ps.conns = cleaned
}

// pairRelayHandler accepts up to two ControlClient connections, completes
// each registration, then forwards every binary Frame between them.
func pairRelayHandler(t *testing.T, ps *pairState) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Registration exchange.
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := UnmarshalControl(raw)
		if err != nil {
			return
		}
		reg, ok := msg.(*Register)
		if !ok || reg.Token == "" {
			return
		}
		data, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("wg-pair-relay"),
		})
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}

		// Register this WS connection with the pair relay.
		ps.mu.Lock()
		ps.conns = append(ps.conns, conn)
		ps.mu.Unlock()

		// On exit, remove this connection from the pair relay's set.
		// This prevents stale connections from being selected as the
		// "peer" after a WebSocket reconnect creates a new handler.
		defer ps.removeConn(conn)

		// Wait for the peer to arrive (when len(ps.conns) >= 2).
		// Uses polling instead of a channel signal to avoid the race
		// between close(ch) and a concurrent select on <-ch that would
		// leave the first handler permanently blocked until a 10s timeout.
		peerDeadline := time.Now().Add(10 * time.Second)
		for {
			peer := ps.findPeer(conn)
			if peer != nil {
				break
			}
			if !time.Now().Before(peerDeadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Forward binary frames to the peer; record for assertions.
		for {
			mt, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			if f, ferr := UnmarshalFrame(raw); ferr == nil {
				ps.record(f)
			}
			if peer := ps.findPeer(conn); peer != nil {
				if err := peer.WriteMessage(websocket.BinaryMessage, raw); err != nil {
					return
				}
			}
		}
	}
}

func startPairedRelay(t *testing.T) (*pairState, string) {
	t.Helper()
	ps := &pairState{}
	srv, url := startFakeRelay(t, pairRelayHandler(t, ps))
	t.Cleanup(srv.Close)
	return ps, url
}

// ── real WG device harness ───────────────────────────────────────────────────

// testKeypair is an X25519 keypair for a real WG device.
type testKeypair struct {
	privHex, pubHex string
}

// newTestKeypair generates a fresh X25519 keypair.
func newTestKeypair(t *testing.T) testKeypair {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return testKeypair{
		privHex: hex.EncodeToString(k.Bytes()),
		pubHex:  hex.EncodeToString(k.PublicKey().Bytes()),
	}
}

// startWgDevice creates a real userspace WG device bound to a RelayTransport
// and returns the device plus its netstack handle. overlay is the device's
// own overlay address; peerPub is the remote public key; endpoint is the
// relay endpoint string (e.g. "relay:1") the device routes to the peer
// through; peerAllowed is the remote overlay prefix.
func startWgDevice(t *testing.T, tr *RelayTransport, keys testKeypair, overlay netip.Addr, peerPub, endpoint, peerAllowed string) (*device.Device, *netstack.Net) {
	t.Helper()

	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{overlay},
		nil, // no DNS resolvers
		1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN(%s): %v", overlay, err)
	}

	logger := device.NewLogger(device.LogLevelError, "wg-test: ")
	dev := device.NewDevice(tun, tr, logger)

	uapi := "private_key=" + keys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key): %v", err)
	}
	peer := "public_key=" + peerPub + "\n" +
		"endpoint=" + endpoint + "\n" +
		"allowed_ip=" + peerAllowed + "\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("Up(): %v", err)
	}

	t.Cleanup(func() {
		dev.Close()
		_ = tr.Close()
	})
	return dev, tnet
}

// waitHandshake polls the device until at least one peer reports a completed
// handshake (last_handshake_time_nsec != 0), or the timeout expires.
// On timeout it dumps WG device state for diagnosis.
func waitHandshake(t *testing.T, dev *device.Device, label string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := dev.IpcGet()
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "last_handshake_time_nsec=") {
					val := strings.TrimPrefix(line, "last_handshake_time_nsec=")
					if val != "0" && val != "" {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Dump device state for diagnosis.
	ipc, _ := dev.IpcGet()
	t.Fatalf("[%s] handshake did not complete within %v; device state:\n%s",
		label, timeout, trimLines(ipc))
}

// trimLines returns s with trailing whitespace stripped from each line
// and empty trailing lines removed, capped at 40 lines.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	limit := 40
	if len(lines) > limit {
		lines = append(lines[:limit], "...")
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " 	\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// waitAllPeers polls the pairState until both relay handler goroutines
// have registered their WebSocket connections, confirming the forwarding
// path is established before any WG device starts sending.
func waitAllPeers(t *testing.T, ps *pairState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps.peerCount() >= 2 {
			// Both handlers registered — forwarding path ready.
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("relay: only %d peer(s) registered (want 2)", ps.peerCount())
}

// ── the integration proof ────────────────────────────────────────────────────

func TestRelayTransport_RealWireGuardHandshakeAndTraffic(t *testing.T) {
	ps, url := startPairedRelay(t)

	// Two ControlClients, one per WG device.
	startClient := func(label string) *ControlClient {
		cfg := DefaultClientConfig()
		cfg.RelayURL = url
		cfg.RegistrationToken = "wg-token"
		cfg.HandshakeTimeout = 2 * time.Second
		// ReadTimeout must comfortably exceed the WG handshake retry cadence
		// (REKEY_TIMEOUT = 5s between initiation attempts); otherwise the client
		// flags the quiet pre-handshake connection as dead and enters reconnect
		// churn, dropping initiations and stalling the handshake. PingInterval
		// refreshes the read deadline more often than ReadTimeout expires.
		cfg.ReadTimeout = 15 * time.Second
		cfg.PingInterval = 5 * time.Second
		client := NewControlClient(cfg)
		if err := client.Start(context.Background()); err != nil {
			t.Fatalf("[%s] Start(): %v", label, err)
		}
		t.Cleanup(func() { client.Shutdown() })
		waitConnected(t, client, label)
		return client
	}

	clientA := startClient("A")
	clientB := startClient("B")

	trA := NewRelayTransport(clientA)
	trB := NewRelayTransport(clientB)
	// Open registers the frame handlers and yields the receive funcs the WG
	// device will drive.
	if _, _, err := trA.Open(0); err != nil {
		t.Fatalf("A Open(): %v", err)
	}
	if _, _, err := trB.Open(0); err != nil {
		t.Fatalf("B Open(): %v", err)
	}

	// Wait until both relay handler goroutines have registered and entered
	// the forwarding loop, before creating a WG device that sends frames.
	waitAllPeers(t, ps)

	// Real WG devices: A is fd00::1, B is fd00::2. Each routes to the other
	// over a distinct relay route (relay:1 and relay:2).
	keysA := newTestKeypair(t)
	keysB := newTestKeypair(t)
	devA, netA := startWgDevice(t, trA, keysA, netip.MustParseAddr("fd00::1"), keysB.pubHex, "relay:1", "fd00::2/128")
	devB, netB := startWgDevice(t, trB, keysB, netip.MustParseAddr("fd00::2"), keysA.pubHex, "relay:2", "fd00::1/128")

	// Real WG handshake must complete through the Relay-backed binds.
	// Use a 25s timeout (3 retries * 5s + 10s headroom) instead of the
	// earlier 20s to provide margin for a REKEY_TIMEOUT boundary crossing.
	waitHandshake(t, devA, "A", 25*time.Second)
	waitHandshake(t, devB, "B", 25*time.Second)

	// Prove encrypted traffic flows end-to-end: a UDP datagram written into
	// A's netstack must arrive at B's netstack, having been encrypted by the
	// real WG device, carried as opaque frames over the relay, and decrypted
	// by B's real WG device.
	const port = 7777
	recvCh := make(chan string, 1)
	listener, err := netB.ListenUDPAddrPort(netip.AddrPortFrom(netip.MustParseAddr("fd00::2"), port))
	if err != nil {
		t.Fatalf("B ListenUDP: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		buf := make([]byte, 256)
		n, _, rerr := listener.ReadFrom(buf)
		if rerr == nil {
			recvCh <- string(buf[:n])
		}
	}()

	sender, err := netA.DialUDPAddrPort(netip.AddrPort{}, netip.AddrPortFrom(netip.MustParseAddr("fd00::2"), port))
	if err != nil {
		t.Fatalf("A DialUDP: %v", err)
	}
	t.Cleanup(func() { sender.Close() })

	const payload = "hello-over-relay-wg"
	if _, err := sender.Write([]byte(payload)); err != nil {
		t.Fatalf("A Write: %v", err)
	}

	deadline := time.Now().Add(25 * time.Second)
	received := false
	for !received && time.Now().Before(deadline) {
		select {
		case got := <-recvCh:
			if got != payload {
				t.Fatalf("B received %q, want %q", got, payload)
			}
			received = true
		case <-time.After(5 * time.Second):
		}
	}
	if !received {
		nf := len(ps.forwardedFrames())
		np := ps.peerCount()
		ipa, _ := devA.IpcGet()
		ipb, _ := devB.IpcGet()
		t.Fatalf("B did not receive UDP datagram through the relay-backed WG bind after 30s (frames=%d peers=%d; devA:\n%s\ndevB:\n%s)",
			nf, np, trimLines(ipa), trimLines(ipb))
	}

	// RouteID preservation end-to-end: every frame forwarded by the Relay
	// must carry a nonzero RouteID (the runtime route it travelled on).
	frames := ps.forwardedFrames()
	if len(frames) == 0 {
		t.Fatal("relay forwarded no frames; real WG device never emitted through the bind")
	}
	var sawRoute1, sawRoute2 bool
	for _, f := range frames {
		if f.RouteID == 0 || f.RouteID > MaxRouteID {
			t.Fatalf("frame with invalid RouteID %d forwarded by relay", f.RouteID)
		}
		if f.RouteID == 1 {
			sawRoute1 = true
		}
		if f.RouteID == 2 {
			sawRoute2 = true
		}
	}
	if !sawRoute1 || !sawRoute2 {
		t.Errorf("expected frames on both relay routes: route1=%v route2=%v", sawRoute1, sawRoute2)
	}
}
