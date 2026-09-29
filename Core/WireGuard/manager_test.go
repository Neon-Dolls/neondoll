package wireguard

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	network "github.com/Neon-Dolls/neondoll/Core/Network"
)

// mockTunnel implements Tunnel for testing.
type mockTunnel struct {
	startCfg  *Config
	peers     []PeerConfig
	started   atomic.Bool
	startErr  error
	stopErr   error
	reconfErr error
}

func newMockTunnel() *mockTunnel {
	return &mockTunnel{}
}

func (m *mockTunnel) Start(_ context.Context, cfg Config) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.startCfg = &cfg
	m.started.Store(true)
	return nil
}

func (m *mockTunnel) Stop() error {
	if m.stopErr != nil {
		return m.stopErr
	}
	m.started.Store(false)
	return nil
}

func (m *mockTunnel) ReconfigurePeers(peers []PeerConfig) error {
	if m.reconfErr != nil {
		return m.reconfErr
	}
	m.peers = append([]PeerConfig(nil), peers...)
	return nil
}

func (m *mockTunnel) LocalAddress() netip.Addr {
	if m.startCfg != nil {
		return m.startCfg.OverlayAddress
	}
	return netip.Addr{}
}

// mockStore implements NetworkStore in memory for test fixtures.
type mockStore struct {
	memberships []*network.Membership
}

func newMockStore(mems ...*network.Membership) *mockStore {
	return &mockStore{memberships: mems}
}

func (s *mockStore) SaveNetwork(_ context.Context, _ *network.Network) error {
	return nil
}

func (s *mockStore) LoadNetwork(_ context.Context) (*network.Network, error) {
	return nil, nil
}

func (s *mockStore) SaveMembership(_ context.Context, _ *network.Membership) error {
	return nil
}

func (s *mockStore) LoadMembership(_ context.Context, _ network.PeerID) (*network.Membership, error) {
	return nil, nil
}

func (s *mockStore) ListMemberships(_ context.Context) ([]*network.Membership, error) {
	return s.memberships, nil
}

func (s *mockStore) DeleteMembership(_ context.Context, _ network.PeerID) error {
	return nil
}

func TestManagerStartStop(t *testing.T) {
	tun := newMockTunnel()
	store := newMockStore()

	mgr := NewManager(tun, store, nil)

	nw := testNetwork(t)

	ctx := context.Background()
	if err := mgr.Start(ctx, nw); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	if !tun.started.Load() {
		t.Fatal("expected tunnel to be started")
	}

	if tun.startCfg == nil {
		t.Fatal("expected Start to be called with config")
	}

	if tun.startCfg.OverlayAddress != nw.Core.OverlayAddress {
		t.Errorf("overlay address: got %s, want %s",
			tun.startCfg.OverlayAddress, nw.Core.OverlayAddress)
	}

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}

	if tun.started.Load() {
		t.Fatal("expected tunnel to be stopped")
	}
}

func TestManagerStartLoadsActivePeers(t *testing.T) {
	bodyKey := [32]byte{3, 4, 5, 6}
	bodyAddr := network.MustParseIPv6("fd01:2345:6789:abcd::2")

	active := &network.Membership{
		BodyID:             "body-1",
		PeerID:             "peer-1",
		WireGuardPublicKey: (*network.WireGuardPublicKey)(&bodyKey),
		OverlayAddress:     bodyAddr,
		Status:             network.MembershipActive,
	}

	pending := &network.Membership{
		BodyID:             "body-2",
		PeerID:             "peer-2",
		WireGuardPublicKey: (*network.WireGuardPublicKey)(&[32]byte{7, 8, 9, 10}),
		OverlayAddress:     network.MustParseIPv6("fd01:2345:6789:abcd::3"),
		Status:             network.MembershipPending,
	}

	store := newMockStore(active, pending)
	tun := newMockTunnel()
	mgr := NewManager(tun, store, nil)

	ctx := context.Background()
	if err := mgr.Start(ctx, testNetwork(t)); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	if len(tun.peers) != 1 {
		t.Fatalf("expected 1 active peer, got %d", len(tun.peers))
	}

	if tun.peers[0].PublicKey != bodyKey {
		t.Errorf("peer public key mismatch")
	}

	expectedAllowed := []netip.Prefix{
		netip.PrefixFrom(bodyAddr, 128),
	}
	if !prefixesEqual(tun.peers[0].AllowedIPs, expectedAllowed) {
		t.Errorf("allowed IPs: got %v, want %v",
			tun.peers[0].AllowedIPs, expectedAllowed)
	}

	if tun.peers[0].PersistentKeepalive != 25*time.Second {
		t.Errorf("keepalive: got %v, want 25s",
			tun.peers[0].PersistentKeepalive)
	}
}

func TestManagerRefreshPeers(t *testing.T) {
	tun := newMockTunnel()
	store := newMockStore()
	mgr := NewManager(tun, store, nil)

	ctx := context.Background()
	if err := mgr.Start(ctx, testNetwork(t)); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Initially no peers.
	if len(tun.peers) != 0 {
		t.Fatalf("expected 0 peers initially, got %d", len(tun.peers))
	}

	// Add a membership to the store.
	key := [32]byte{11, 12, 13, 14}
	addr := network.MustParseIPv6("fd01:2345:6789:abcd::5")
	store.memberships = []*network.Membership{
		{
			BodyID:             "body-3",
			PeerID:             "peer-3",
			WireGuardPublicKey: (*network.WireGuardPublicKey)(&key),
			OverlayAddress:     addr,
			Status:             network.MembershipActive,
		},
	}

	if err := mgr.RefreshPeers(ctx); err != nil {
		t.Fatalf("RefreshPeers() failed: %v", err)
	}

	if len(tun.peers) != 1 {
		t.Fatalf("expected 1 peer after refresh, got %d", len(tun.peers))
	}
}

func TestManagerStopWhenNotStarted(t *testing.T) {
	tun := newMockTunnel()
	store := newMockStore()
	mgr := NewManager(tun, store, nil)

	// Should not panic or error.
	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop() should not error when not started: %v", err)
	}
}

func TestManagerSkipsNilPublicKey(t *testing.T) {
	addr := network.MustParseIPv6("fd01:2345:6789:abcd::10")
	member := &network.Membership{
		BodyID:             "body-nokey",
		PeerID:             "peer-nokey",
		WireGuardPublicKey: nil,
		OverlayAddress:     addr,
		Status:             network.MembershipActive,
	}

	store := newMockStore(member)
	tun := newMockTunnel()
	mgr := NewManager(tun, store, nil)

	ctx := context.Background()
	if err := mgr.Start(ctx, testNetwork(t)); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Membership without a public key must be skipped.
	if len(tun.peers) != 0 {
		t.Fatalf("expected 0 peers (nil key skipped), got %d", len(tun.peers))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func testNetwork(t *testing.T) *network.Network {
	t.Helper()
	nw, err := network.NewNetwork("test-network")
	if err != nil {
		t.Fatalf("NewNetwork failed: %v", err)
	}
	return nw
}

func prefixesEqual(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
