// Package wireguard provides a real userspace WireGuard tunnel implementation
// using golang.zx2c4.com/wireguard with tun/netstack, and a Manager that
// bridges Doll Network membership state to WireGuard peer configuration.
package wireguard

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Network"
)

// ── Manager ──────────────────────────────────────────────────────────────────

// The Manager's store interface is satisfied by anything that can list
// persisted memberships. The real NetworkStore from Core/Network satisfies this.
type managerStore interface {
	ListMemberships(ctx context.Context) ([]*network.Membership, error)
}

// Manager bridges Network state to WireGuard tunnel peer configuration.
// It subscribes to membership changes and reconfigures peers accordingly.
type Manager struct {
	tunnel Tunnel
	store  managerStore

	log *slog.Logger

	mu       sync.Mutex
	started  bool
	stopOnce sync.Once

	overlayAddr netip.Addr
	listenPort  int
}

// NewManager creates a new Manager.
func NewManager(tunnel Tunnel, store managerStore, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		tunnel: tunnel,
		store:  store,
		log:    log.With("component", "wireguard.manager"),
	}
}

// Start initialises the tunnel and configures initial peers from the given
// network state. Returns an error if the tunnel fails to start or if the
// initial peer reconfiguration fails.
func (m *Manager) Start(ctx context.Context, nw *network.Network) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return fmt.Errorf("wireguard: manager already started")
	}

	cfg := Config{
		PrivateKey:     [32]byte(nw.Core.PrivateKey),
		ListenPort:     m.listenPort,
		OverlayAddress: nw.Core.OverlayAddress,
		OverlayPrefix:  nw.Core.OverlayPrefix,
		MTU:            DefaultMTU,
	}

	if err := m.tunnel.Start(ctx, cfg); err != nil {
		return fmt.Errorf("wireguard: tunnel start: %w", err)
	}

	// Refresh peers is NOT optional — fail the whole start if it fails,
	// so the caller knows the overlay is not ready.
	if err := m.refreshPeers(ctx); err != nil {
		// Best-effort tear down the tunnel we just started.
		m.tunnel.Stop()
		return fmt.Errorf("wireguard: initial peer config: %w", err)
	}

	m.started = true
	m.log.Info("wireguard manager started",
		"network_id", nw.NetworkID,
		"overlay_addr", nw.Core.OverlayAddress.String(),
	)
	return nil
}

// Stop tears down the WireGuard tunnel and manager.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var err error
	m.stopOnce.Do(func() {
		if !m.started {
			return
		}
		err = m.tunnel.Stop()
		m.started = false
		m.log.Info("wireguard manager stopped")
	})
	return err
}

// RefreshPeers reloads membership state from the store and reconfigures
// all WireGuard peers. Safe to call on a running manager.
func (m *Manager) RefreshPeers(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return fmt.Errorf("wireguard: manager not started")
	}
	return m.refreshPeers(ctx)
}

// refreshPeers does the actual peer reconfiguration (no started-check).
// Caller must hold m.mu.
func (m *Manager) refreshPeers(ctx context.Context) error {
	memberships, err := m.store.ListMemberships(ctx)
	if err != nil {
		return fmt.Errorf("wireguard: list memberships: %w", err)
	}

	peers := m.buildPeerConfigs(memberships)
	if err := m.tunnel.ReconfigurePeers(peers); err != nil {
		return fmt.Errorf("wireguard: reconfigure peers: %w", err)
	}

	// Store a peer snapshot for diagnostics.
	if rt, ok := m.tunnel.(*RealTunnel); ok {
		rt.WithPeers(peers)
	}

	m.log.Debug("peers refreshed", "count", len(peers))
	return nil
}

// buildPeerConfigs converts a slice of Memberships into WireGuard PeerConfigs.
func (m *Manager) buildPeerConfigs(memberships []*network.Membership) []PeerConfig {
	peers := make([]PeerConfig, 0, len(memberships))
	for _, mem := range memberships {
		if mem.PeerID == "" || mem.WireGuardPublicKey == nil || mem.Status != network.MembershipActive {
			continue
		}

		// Build allowed IPs from the overlay address.
		allowedIPs := []netip.Prefix{}
		if mem.OverlayAddress.IsValid() {
			prefix := netip.PrefixFrom(mem.OverlayAddress, 128)
			allowedIPs = append(allowedIPs, prefix)
		}

		var pub [32]byte
		copy(pub[:], mem.WireGuardPublicKey[:])

		pc := PeerConfig{
			PublicKey:           pub,
			AllowedIPs:          allowedIPs,
			Endpoint:            mem.Endpoint,
			PersistentKeepalive: DefaultPersistentKeepaliveInterval,
		}
		peers = append(peers, pc)
	}
	return peers
}

// Diagnostics returns WireGuard tunnel diagnostics.
func (m *Manager) Diagnostics(_ context.Context) (*DiagnosticsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil, fmt.Errorf("wireguard: manager not started")
	}

	return m.tunnel.Diagnostics()
}
