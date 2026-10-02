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
	ready chan struct{}

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

		ps.mu.Lock()
		first := len(ps.conns) == 0
		ps.conns = append(ps.conns, conn)
		if len(ps.conns) == 2 {
			close(ps.ready)
		}
		ps.mu.Unlock()

		if first {
			// First client waits for the second to arrive before forwarding.
			select {
			case <-ps.ready:
			case <-time.After(10 * time.Second):
				return
			}
		}

		ps.mu.Lock()
		var peer *websocket.Conn
		for _, c := range ps.conns {
			if c != conn {
				peer = c
				break
			}
		}
		ps.mu.Unlock()
		if peer == nil {
			return
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
			if err := peer.WriteMessage(websocket.BinaryMessage, raw); err != nil {
				return
			}
		}
	}
}

func startPairedRelay(t *testing.T) (*pairState, string) {
	t.Helper()
	ps := &pairState{ready: make(chan struct{})}
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
	t.Fatalf("[%s] handshake did not complete within %v", label, timeout)
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

	// Real WG devices: A is fd00::1, B is fd00::2. Each routes to the other
	// over a distinct relay route (relay:1 and relay:2).
	keysA := newTestKeypair(t)
	keysB := newTestKeypair(t)
	devA, netA := startWgDevice(t, trA, keysA, netip.MustParseAddr("fd00::1"), keysB.pubHex, "relay:1", "fd00::2/128")
	devB, netB := startWgDevice(t, trB, keysB, netip.MustParseAddr("fd00::2"), keysA.pubHex, "relay:2", "fd00::1/128")

	// Real WG handshake must complete through the Relay-backed binds.
	waitHandshake(t, devA, "A", 20*time.Second)
	waitHandshake(t, devB, "B", 20*time.Second)

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

	select {
	case got := <-recvCh:
		if got != payload {
			t.Fatalf("B received %q, want %q", got, payload)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("B did not receive UDP datagram through the relay-backed WG bind")
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
