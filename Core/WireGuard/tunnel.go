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
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ── Defaults ─────────────────────────────────────────────────────────────────

const (
	// DefaultMTU is the default MTU for the WireGuard overlay. 1280 ensures
	// IPv6 minimum MTU compliance and avoids fragmentation on IPv4 as well.
	DefaultMTU = 1280

	// DefaultPersistentKeepaliveInterval is the default interval for
	// persistent keepalive pings. 25s is a common default for NAT traversal.
	DefaultPersistentKeepaliveInterval = 25 * time.Second
)

// ── Interfaces and types ────────────────────────────────────────────────────

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
	// Netstack returns the netstack.Net for overlay networking (TCP/UDP).
	// Returns nil if the tunnel is not started.
	Netstack() *netstack.Net
	// Diagnostics returns current WireGuard device diagnostics.
	Diagnostics() (*DiagnosticsResult, error)
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
	// MTU is the tunnel MTU. Zero means DefaultMTU.
	MTU int
	// PeerConfigs is a snapshot of the last applied peer configs, used for
	// diagnostics. It is not used during Start; it is set by WithPeers.
	PeerConfigs []PeerConfig
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

// DiagnosticsResult contains current WireGuard device diagnostics.
type DiagnosticsResult struct {
	// Peers maps public keys to per-peer diagnostic info.
	Peers []PeerDiagnostics `json:"peers,omitempty"`
}

// PeerDiagnostics contains diagnostics for a single WireGuard peer.
type PeerDiagnostics struct {
	// PublicKey is the peer's public key (hex-encoded).
	PublicKey string `json:"public_key"`
	// Endpoint is the actual UDP endpoint, if connected.
	Endpoint string `json:"endpoint,omitempty"`
	// HandshakeTime is the time of last completed handshake, or empty.
	HandshakeTime string `json:"handshake_time,omitempty"`
	// HandshakePending is true if no handshake has completed yet.
	HandshakePending bool `json:"handshake_pending,omitempty"`
	// TxBytes is the number of bytes transmitted to this peer.
	TxBytes int64 `json:"tx_bytes,omitempty"`
	// RxBytes is the number of bytes received from this peer.
	RxBytes int64 `json:"rx_bytes,omitempty"`
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

	// bind is an optional custom transport for WireGuard packets.
	// When non-nil, it replaces the default UDP conn.NewDefaultBind().
	// Used by M4.5 RelayTransport; nil preserves direct M3 operation.
	bind conn.Bind
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

	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}

	// Create the netstack-based virtual TUN device.
	// The local address is our overlay address; no DNS needed for the tunnel.
	tunDev, net, err := netstack.CreateNetTUN(
		[]netip.Addr{cfg.OverlayAddress},
		[]netip.Addr{}, // no DNS
		mtu,
	)
	if err != nil {
		return fmt.Errorf("wireguard: create netstack tun: %w", err)
	}
	t.tunDevice = tunDev
	t.net = net

	// Create the WireGuard device. Use custom bind if configured
	// (e.g. RelayTransport from M4.5), otherwise default UDP bind.
	bind := t.bind
	if bind == nil {
		bind = conn.NewDefaultBind()
	}
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
	t.dev = nil
	t.net = nil
	t.tunDevice = nil
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

// Netstack returns the netstack.Net for overlay networking.
func (t *RealTunnel) Netstack() *netstack.Net {
	return t.net
}

// Diagnostics returns current WireGuard device diagnostics parsed from UAPI
// output. Returns per-peer endpoint, handshake state, tx bytes, and rx bytes.
// Private keys are never exposed.
func (t *RealTunnel) Diagnostics() (*DiagnosticsResult, error) {
	if !t.started || t.dev == nil {
		return nil, fmt.Errorf("wireguard: tunnel not started")
	}

	raw, err := t.dev.IpcGet()
	if err != nil {
		return nil, fmt.Errorf("wireguard: ipc get: %w", err)
	}

	result := &DiagnosticsResult{
		Peers: make([]PeerDiagnostics, 0),
	}

	// IpcGet output: interface block followed by peer blocks separated by
	// empty lines. Each peer block begins with public_key=... and carries:
	//   endpoint=<host:port>
	//   last_handshake_time_sec=<sec>
	//   last_handshake_time_nsec=<nsec>
	//   tx_bytes=<n>
	//   rx_bytes=<n>
	blocks := strings.Split(raw, "\n\n")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" || !strings.Contains(block, "public_key=") {
			continue
		}

		var pd PeerDiagnostics
		var hsSec int64
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			before, after, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch before {
			case "public_key":
				pd.PublicKey = after
			case "endpoint":
				pd.Endpoint = after
			case "last_handshake_time_sec":
				if s, parseErr := strconv.ParseInt(after, 10, 64); parseErr == nil {
					hsSec = s
				}
			case "last_handshake_time_nsec":
				if after == "" || after == "0" {
					pd.HandshakePending = true
				} else {
					pd.HandshakePending = false
					if nsec, parseErr := strconv.ParseInt(after, 10, 64); parseErr == nil {
						pd.HandshakeTime = time.Unix(hsSec, nsec).UTC().Format(time.RFC3339Nano)
					}
				}
			case "tx_bytes":
				if n, parseErr := strconv.ParseUint(after, 10, 64); parseErr == nil {
					pd.TxBytes = int64(n)
				}
			case "rx_bytes":
				if n, parseErr := strconv.ParseUint(after, 10, 64); parseErr == nil {
					pd.RxBytes = int64(n)
				}
			}
		}
		result.Peers = append(result.Peers, pd)
	}

	return result, nil
}

// ── UAPI config builders ─────────────────────────────────────────────────────

// buildUAPIConfig assembles the full UAPI config string including interface
// settings and optional peer blocks. Keys are hex-encoded (lowercase hex),
// matching the WireGuard UAPI protocol.
// Note: MTU is not set via UAPI — it's configured during TUN creation.
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

// WaitHandshake blocks until at least one peer completes a handshake,
// or the context expires or the given timeout is reached.
func (t *RealTunnel) WaitHandshake(ctx context.Context, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wireguard: handshake timeout (%v)", timeout)
		case <-ticker.C:
			ok, err := t.HasHandshake()
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

// WithPeers stores a snapshot of peer configs for diagnostics use.
// This is set by the Manager after reconfiguration.
func (t *RealTunnel) WithPeers(peers []PeerConfig) {
	t.cfg.PeerConfigs = peers
}

// HasHandshake returns true if at least one peer has completed a WireGuard
// handshake, by inspecting the device's IpcGet output.
func (t *RealTunnel) HasHandshake() (bool, error) {
	if !t.started || t.dev == nil {
		return false, fmt.Errorf("wireguard: tunnel not started")
	}
	out, err := t.dev.IpcGet()
	if err != nil {
		return false, fmt.Errorf("wireguard: ipc get: %w", err)
	}
	return hasHandshakeFromIpc(out), nil
}

// hasHandshakeFromIpc scans IpcGet output for a non-zero handshake timestamp.
func hasHandshakeFromIpc(out string) bool {
	prefix := "last_handshake_time_nsec="
	for i := 0; i <= len(out)-len(prefix); i++ {
		if out[i:i+len(prefix)] == prefix {
			j := i + len(prefix)
			for j < len(out) && out[j] != '\n' && out[j] != '\r' {
				j++
			}
			val := out[i+len(prefix) : j]
			if val != "" && val != "0" {
				return true
			}
		}
	}
	return false
}
