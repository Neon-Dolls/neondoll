// Package wireguard provides a real userspace WireGuard tunnel implementation
// using golang.zx2c4.com/wireguard with tun/netstack, and a Manager that
// bridges Doll Network membership state to WireGuard peer configuration.
//
// The tunnel runs entirely in userspace (no TUN device, no root required)
// via netstack, making it suitable for testing and production alike.
package wireguard

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ── Interfaces and types ─────────────────────────────────────────────────────

// Tunnel manages a real WireGuard tunnel device.
type Tunnel interface {
	// Start creates and configures the WireGuard device with the given config.
	Start(ctx context.Context, cfg Config) error
	// Stop tears down the WireGuard device and releases resources.
	Stop() error
	// ReconfigurePeers applies a new peer list, replacing all existing peers.
	ReconfigurePeers(peers []PeerConfig) error
	// LocalAddress returns the overlay IPv6 address of this endpoint.
	LocalAddress() netip.Addr
}

// Config holds the WireGuard interface configuration for the local endpoint.
type Config struct {
	// PrivateKey is the Curve25519 private key for this endpoint.
	PrivateKey [32]byte
	// ListenPort is the UDP port for WireGuard (0 = auto).
	ListenPort int
	// OverlayAddress is this endpoint's own ULA /128 IPv6 address.
	OverlayAddress netip.Addr
	// OverlayPrefix is the ULA prefix to route through the tunnel (e.g. fd00::/8).
	OverlayPrefix netip.Prefix
}

// PeerConfig describes a remote WireGuard peer.
type PeerConfig struct {
	// PublicKey is the remote peer's Curve25519 public key.
	PublicKey [32]byte
	// AllowedIPs lists the IP prefixes that should be routed to this peer.
	AllowedIPs []netip.Prefix
	// Endpoint is the UDP address ("host:port") of the remote peer, if known.
	Endpoint string
	// PersistentKeepalive is the interval for keepalive pings (0 = off).
	PersistentKeepalive time.Duration
}

// ── Real tunnel implementation (netstack / userspace) ─────────────────────────

// RealTunnel is a production WireGuard tunnel using golang.zx2c4.com/wireguard
// with a userspace netstack TUN. No kernel TUN device or root required.
type RealTunnel struct {
	dev       *device.Device
	net       *netstack.Net
	tunDevice tun.Device

	cfg     Config
	log     *slog.Logger
	started bool
}

// NewRealTunnel creates a new RealTunnel with the given logger.
func NewRealTunnel(log *slog.Logger) *RealTunnel {
	if log == nil {
		log = slog.Default()
	}
	return &RealTunnel{log: log.With("component", "wireguard.tunnel")}
}

// Start creates the netstack TUN, WireGuard device, applies config, and
// brings the interface up.
func (t *RealTunnel) Start(ctx context.Context, cfg Config) error {
	if t.started {
		return fmt.Errorf("wireguard: tunnel already started")
	}

	t.cfg = cfg

	// Create the netstack-based virtual TUN device.
	// The local address is our overlay address; no DNS needed for the tunnel.
	tunDev, net, err := netstack.CreateNetTUN(
		[]netip.Addr{cfg.OverlayAddress},
		[]netip.Addr{}, // no DNS
		device.DefaultMTU,
	)
	if err != nil {
		return fmt.Errorf("wireguard: create netstack tun: %w", err)
	}
	t.tunDevice = tunDev
	t.net = net

	// Create the WireGuard device with a default UDP bind.
	bind := conn.NewDefaultBind()
	logger := device.NewLogger(
		device.LogLevelError,
		fmt.Sprintf("(%s) ", cfg.OverlayAddress.String()),
	)

	dev := device.NewDevice(tunDev, bind, logger)

	// Build the UAPI configuration string (keys are hex-encoded).
	uapi := t.buildUAPIConfig(cfg, nil)

	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: ipc set config: %w", err)
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: bring up: %w", err)
	}

	t.dev = dev
	t.started = true
	t.log.Info("wireguard tunnel started",
		"overlay_address", cfg.OverlayAddress.String(),
		"listen_port", cfg.ListenPort,
	)

	return nil
}

// Stop tears down the WireGuard device.
func (t *RealTunnel) Stop() error {
	if !t.started {
		return nil
	}
	t.dev.Close()
	t.started = false
	t.log.Info("wireguard tunnel stopped")
	return nil
}

// ReconfigurePeers replaces all peers on the device with the given list.
func (t *RealTunnel) ReconfigurePeers(peers []PeerConfig) error {
	if !t.started {
		return fmt.Errorf("wireguard: tunnel not started")
	}

	// Remove all existing peers first.
	t.dev.RemoveAllPeers()

	// Build a config string containing only the peer blocks.
	uapi := t.buildPeerConfigs(peers)
	if uapi == "" {
		return nil
	}

	if err := t.dev.IpcSet(uapi); err != nil {
		return fmt.Errorf("wireguard: ipc set peers: %w", err)
	}

	t.log.Debug("wireguard peers reconfigured", "count", len(peers))
	return nil
}

// LocalAddress returns the overlay address from the tunnel config.
func (t *RealTunnel) LocalAddress() netip.Addr {
	return t.cfg.OverlayAddress
}

// ── UAPI config builders ─────────────────────────────────────────────────────

// buildUAPIConfig assembles the full UAPI config string including interface
// settings and optional peer blocks. Keys are hex-encoded (lowercase hex),
// matching the WireGuard UAPI protocol.
func (t *RealTunnel) buildUAPIConfig(cfg Config, peers []PeerConfig) string {
	uapi := fmt.Sprintf("private_key=%s\n", hex.EncodeToString(cfg.PrivateKey[:]))

	if cfg.ListenPort > 0 {
		uapi += fmt.Sprintf("listen_port=%d\n", cfg.ListenPort)
	}

	if peers != nil {
		uapi += t.buildPeerConfigs(peers)
	}

	return uapi
}

// buildPeerConfigs builds the UAPI config string for a set of peers.
func (t *RealTunnel) buildPeerConfigs(peers []PeerConfig) string {
	var uapi string
	for _, p := range peers {
		uapi += fmt.Sprintf("public_key=%s\n", hex.EncodeToString(p.PublicKey[:]))

		for _, ip := range p.AllowedIPs {
			uapi += fmt.Sprintf("allowed_ip=%s\n", ip.String())
		}

		if p.Endpoint != "" {
			uapi += fmt.Sprintf("endpoint=%s\n", p.Endpoint)
		}

		if p.PersistentKeepalive > 0 {
			secs := int(p.PersistentKeepalive.Seconds())
			uapi += fmt.Sprintf("persistent_keepalive_interval=%d\n", secs)
		}

		// Empty line separates peer blocks (UAPI convention).
		uapi += "\n"
	}
	return uapi
}
