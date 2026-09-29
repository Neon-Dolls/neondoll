package wireguard

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	network "github.com/Neon-Dolls/neondoll/Core/Network"
)

// Manager bridges Network state to WireGuard peer configuration.
//
// It loads active memberships from the network store and configures them
// as WireGuard peers on the tunnel. The Manager owns the tunnel lifecycle.
type Manager struct {
	tunnel Tunnel
	store  network.NetworkStore
	log    *slog.Logger
}

// NewManager creates a new Manager backed by the given tunnel and store.
func NewManager(tunnel Tunnel, store network.NetworkStore, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		tunnel: tunnel,
		store:  store,
		log:    log.With("component", "wireguard.manager"),
	}
}

// Start initialises the WireGuard tunnel from the given Core network identity.
// It configures the local endpoint with the Core's private key and overlay
// address, then loads all active memberships from the store and adds them
// as peers.
func (m *Manager) Start(ctx context.Context, nw *network.Network) error {
	m.log.Info("starting wireguard manager",
		"network_id", nw.NetworkID,
		"overlay", nw.Core.OverlayAddress,
	)

	prefix := network.NewIPv6Allocator(nw.NetworkID).Prefix()

	wgCfg := Config{
		PrivateKey:     [32]byte(nw.Core.PrivateKey),
		ListenPort:     51820,
		OverlayAddress: nw.Core.OverlayAddress,
		OverlayPrefix:  prefix,
	}

	if err := m.tunnel.Start(ctx, wgCfg); err != nil {
		return fmt.Errorf("wireguard manager: start tunnel: %w", err)
	}

	// Load peers from store.
	if err := m.RefreshPeers(ctx); err != nil {
		// Log the error but don't fail — tunnel is up, peers can be loaded later.
		m.log.Error("failed to load initial peers", "err", err)
	}

	m.log.Info("wireguard manager started")
	return nil
}

// Stop tears down the WireGuard tunnel.
func (m *Manager) Stop() error {
	m.log.Info("stopping wireguard manager")
	return m.tunnel.Stop()
}

// RefreshPeers reloads all active memberships from the store and applies
// them as WireGuard peers on the tunnel.
func (m *Manager) RefreshPeers(ctx context.Context) error {
	memberships, err := m.store.ListMemberships(ctx)
	if err != nil {
		return fmt.Errorf("wireguard manager: list memberships: %w", err)
	}

	var peers []PeerConfig
	for _, mem := range memberships {
		if mem.Status != network.MembershipActive {
			continue
		}

		if mem.WireGuardPublicKey == nil {
			m.log.Warn("active membership missing wireguard public key",
				"peer_id", mem.PeerID,
				"body_id", mem.BodyID,
			)
			continue
		}

		peer := PeerConfig{
			PublicKey: [32]byte(*mem.WireGuardPublicKey),
			AllowedIPs: []netip.Prefix{
				netip.PrefixFrom(mem.OverlayAddress, 128),
			},
			PersistentKeepalive: 25 * time.Second,
		}

		peers = append(peers, peer)
	}

	if err := m.tunnel.ReconfigurePeers(peers); err != nil {
		return fmt.Errorf("wireguard manager: reconfigure peers: %w", err)
	}

	m.log.Debug("wireguard peers refreshed", "active_count", len(peers))
	return nil
}

// Tunnel returns the underlying Tunnel for direct access (e.g. registering
// callbacks).
func (m *Manager) Tunnel() Tunnel {
	return m.tunnel
}
