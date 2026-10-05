package body

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// testRecorderBind wraps a conn.Bind and records operations.
type testRecorderBind struct {
	inner  conn.Bind
	opened atomic.Bool
	closed atomic.Bool
	sendN  atomic.Int64
}

func newTestRecorderBind(inner conn.Bind) *testRecorderBind {
	return &testRecorderBind{inner: inner}
}

func (b *testRecorderBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.opened.Store(true)
	return b.inner.Open(port)
}

func (b *testRecorderBind) Close() error {
	b.closed.Store(true)
	return b.inner.Close()
}

func (b *testRecorderBind) SetMark(mark uint32) error {
	return b.inner.SetMark(mark)
}

func (b *testRecorderBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.sendN.Add(int64(len(bufs)))
	return b.inner.Send(bufs, ep)
}

func (b *testRecorderBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return b.inner.ParseEndpoint(s)
}

func (b *testRecorderBind) BatchSize() int {
	return b.inner.BatchSize()
}

// bytesTo32 copies a []byte into a [32]byte, zero-padding if needed.
func bytesTo32(src []byte) [32]byte {
	var dst [32]byte
	copy(dst[:], src)
	return dst
}

// newTestKeys returns a Body keypair and a Core public key for testing.
func newTestKeys() (priv [32]byte, pub [32]byte) {
	kp := generateTestKeypair()
	return bytesTo32(kp.privateKey.bytes), bytesTo32(kp.PublicKey().bytes)
}

// generateTestKeypair returns a WgKeypair for testing.
func generateTestKeypair() *WgKeypair {
	kp, err := GenerateWgKeypair()
	if err != nil {
		panic(fmt.Sprintf("GenerateWgKeypair: %v", err))
	}
	return kp
}

// testTunnelConfig returns a BodyTunnelConfig with auto-generated keys and
// a dummy endpoint. The caller should set CoreEndpoint to a reachable address.
func testTunnelConfig() (BodyTunnelConfig, [32]byte, [32]byte) {
	priv, pub := newTestKeys()
	return BodyTunnelConfig{
		PrivateKey:     priv,
		CorePublicKey:  pub,
		OverlayAddress: netip.MustParseAddr("fd00::2"),
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   "127.0.0.1:1", // unreachable endpoint — handshake won't complete
		ListenPort:     0,
		MTU:            0,
	}, priv, pub
}

func TestBodyTunnel_DefaultBindStartStop(t *testing.T) {
	// A tunnel with nil Bind should create and use a DefaultBind and clean up.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, _, _ := testTunnelConfig()
	log := slog.Default()

	bt := NewBodyTunnel(cfg, log)

	if err := bt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Log("Tunnel started with default UDP bind")

	if !bt.started {
		t.Fatal("expected started=true after Start")
	}

	if err := bt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	t.Log("Tunnel stopped — default bind cleaned up")

	if bt.started {
		t.Fatal("expected started=false after Stop")
	}
}

func TestBodyTunnel_CustomBindStartStop(t *testing.T) {
	// A tunnel with a provided Bind should use it instead of DefaultBind
	// and properly close it on Stop.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, _, _ := testTunnelConfig()

	defaultBind := conn.NewDefaultBind()
	rec := newTestRecorderBind(defaultBind)
	cfg.Bind = rec

	log := slog.Default()

	bt := NewBodyTunnel(cfg, log)

	if err := bt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !rec.opened.Load() {
		t.Fatal("custom bind was not opened by tunnel Start")
	}
	t.Log("Custom bind was opened by tunnel Start")

	if err := bt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !rec.closed.Load() {
		t.Fatal("custom bind was not closed by tunnel Stop")
	}
	t.Log("Custom bind was closed by tunnel Stop")
}

func TestBodyTunnel_BindValuesPassedThrough(t *testing.T) {
	// Verify that the config Bind value is passed as-is to the
	// WireGuard device: the tunnel does not replace or wrap it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, _, _ := testTunnelConfig()

	defaultBind := conn.NewDefaultBind()
	rec := newTestRecorderBind(defaultBind)
	cfg.Bind = rec

	log := slog.Default()

	bt := NewBodyTunnel(cfg, log)
	if err := bt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if rec.sendN.Load() == 0 {
		t.Log("sendN=0 (expected — no peer to send to)")
	}

	if err := bt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, tun/device/bind are all closed. The recorder wraps the
	// exact same bind reference — if it was opened, it was our bind.
	if !rec.opened.Load() {
		t.Fatal("values pass-through: recorder bind was not opened")
	}
	if !rec.closed.Load() {
		t.Fatal("values pass-through: recorder bind was not closed")
	}
}
