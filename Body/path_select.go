// Package body provides WireGuard operations for the Doll Body, including
// initial path selection (M5).
package body

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// PathTunnel is a tunnel created during initial path selection.  It wraps a
// WireGuard device that can be used for overlay traffic once the handshake
// completes.
type PathTunnel interface {
	// Start brings up the WireGuard device and configures the peer.
	Start(ctx context.Context) error
	// Stop shuts down the tunnel.
	Stop() error
	// WaitHandshake blocks until a WireGuard handshake completes or the
	// context deadline expires.
	WaitHandshake(ctx context.Context, timeout time.Duration) error
	// BoundBind returns the transport Bind in use, if any (nil for
	// default-UDP tunnels).
	BoundBind() conn.Bind
	// IpcGet returns the WireGuard device's IPC status for identity and
	// handshake verification.
	IpcGet() (string, error)
	// Netstack returns the userspace TCP/IP stack for overlay traffic.
	Netstack() *netstack.Net
}

// PathSelectorConfig holds the configuration for initial path selection.
type PathSelectorConfig struct {
	// CorePublicKey is Core's WireGuard public key.
	CorePublicKey [32]byte

	// DirectEndpoint is Core's direct UDP WireGuard endpoint ("ip:port").
	DirectEndpoint string

	// RelayWGUDPEndpoint is the Relay's WireGuard UDP endpoint ("ip:port").
	RelayWGUDPEndpoint string

	// RelayWSSURL is the Relay's WSS WebSocket URL for carrying WG frames
	// (e.g., "wss://relay.example.com/ws").
	RelayWSSURL string

	// RouteID is the Relay route identifier.
	RouteID relay.RouteID

	// RouteCredential is the Relay route credential token.
	RouteCredential string

	// HandshakeTimeout is the maximum time to wait for a WireGuard
	// handshake on each path attempt.
	HandshakeTimeout time.Duration

	// TLSConfig optionally provides a TLS configuration for the WSS path.
	// When nil, the default system TLS roots are used.
	TLSConfig *tls.Config
}

// SelectResult describes the outcome of initial path selection.
type SelectResult struct {
	// Err is non-nil when all path attempts failed.
	Err error

	// Path identifies which path succeeded ("direct", "relay-udp", or
	// "relay-wss").  Empty when all paths fail.
	Path string

	// Tunnel is the successful tunnel.  Nil when all paths fail.
	Tunnel PathTunnel

	// BoundBind is the transport Bind of the successful tunnel, if any.
	// Non-nil only for the "relay-wss" path (the BodyWSSBind).
	BoundBind conn.Bind

	// Devices holds the WireGuard devices created during failed path
	// attempts so identity can be verified even on failure.
	Devices map[string]*device.Device
}

// pathAttempt describes one path to try during initial path selection.
type pathAttempt struct {
	label     string
	corePeer  [32]byte
	newTunnel func() (PathTunnel, error)
}

// bodyTunnelI is a concrete tunnel created during path selection.  It creates
// a minimal WireGuard device with the given private key and peer configuration
// on Start(), using the provided Bind or conn.NewDefaultBind() when nil.
type bodyTunnelI struct {
	privateKey   [32]byte
	coreKey      [32]byte
	bind         conn.Bind
	peerEndpoint string
	dev          *device.Device
	net          *netstack.Net
	tun          tun.Device
	closed       bool
}

// newBodyTunnel creates a bodyTunnelI with the given identity and transport.
func newBodyTunnel(bodyKey, coreKey [32]byte, bind conn.Bind) *bodyTunnelI {
	return &bodyTunnelI{
		privateKey: bodyKey,
		coreKey:    coreKey,
		bind:       bind,
	}
}

// Start implements PathTunnel.
func (bt *bodyTunnelI) Start(ctx context.Context) error {
	if bt.dev != nil {
		return fmt.Errorf("body-path: tunnel already started")
	}

	// Create a userspace TUN for this tunnel.  We use a minimal overlay
	// configuration — the selector doesn't need the overlay address; the
	// caller does.  The netstack provides handshake- and identity-check
	// plumbing.
	mtu := 1280
	const overlayAddr = "fd00::2"
	tunDev, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(overlayAddr)},
		nil,
		mtu,
	)
	if err != nil {
		return fmt.Errorf("body-path: create netstack: %w", err)
	}
	bt.tun = tunDev
	bt.net = tnet

	bind := bt.bind
	if bind == nil {
		bind = conn.NewDefaultBind()
	}

	l := device.NewLogger(device.LogLevelError, "body-path: ")
	d := device.NewDevice(tunDev, bind, l)

	// Set private key.
	privHex := hex.EncodeToString(bt.privateKey[:])
	if err := d.IpcSet(fmt.Sprintf("private_key=%s\n", privHex)); err != nil {
		d.Close()
		return fmt.Errorf("body-path: set private key: %w", err)
	}

	// Configure the peer.
	pubHex := hex.EncodeToString(bt.coreKey[:])
	peerCfg := fmt.Sprintf(
		"public_key=%s\nendpoint=%s\npersistent_keepalive_interval=25\nallowed_ip=fd00::1/128\n",
		pubHex,
		bt.peerEndpoint,
	)
	if err := d.IpcSet(peerCfg); err != nil {
		d.Close()
		return fmt.Errorf("body-path: set peer: %w", err)
	}

	d.Up()
	bt.dev = d
	return nil
}

// Stop implements PathTunnel.
func (bt *bodyTunnelI) Stop() error {
	if bt.closed {
		return nil
	}
	if bt.dev != nil {
		bt.dev.Close()
	}
	bt.closed = true
	return nil
}

// WaitHandshake implements PathTunnel.
func (bt *bodyTunnelI) WaitHandshake(ctx context.Context, timeout time.Duration) error {
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
			return fmt.Errorf("body-path: handshake timeout (%v)", timeout)
		case <-ticker.C:
			ok, err := bt.handshakeComplete()
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

// handshakeComplete checks whether the WireGuard handshake has completed.
func (bt *bodyTunnelI) handshakeComplete() (bool, error) {
	if bt.dev == nil {
		return false, nil
	}
	out, err := bt.dev.IpcGet()
	if err != nil {
		return false, fmt.Errorf("body-path: ipc get: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "last_handshake_time_nsec=") {
			val := strings.TrimPrefix(trimmed, "last_handshake_time_nsec=")
			if val != "0" && val != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

// BoundBind implements PathTunnel.
func (bt *bodyTunnelI) BoundBind() conn.Bind { return bt.bind }

// IpcGet implements PathTunnel.
func (bt *bodyTunnelI) IpcGet() (string, error) {
	if bt.dev == nil {
		return "", fmt.Errorf("body-path: tunnel not started")
	}
	return bt.dev.IpcGet()
}

// Netstack implements PathTunnel.
func (bt *bodyTunnelI) Netstack() *netstack.Net { return bt.net }

// PathSelector performs initial path selection.
type PathSelector struct {
	log     *slog.Logger
	devices map[string]*device.Device
}

// NewPathSelector creates a new PathSelector.
func NewPathSelector(log *slog.Logger) *PathSelector {
	if log == nil {
		log = slog.Default()
	}
	return &PathSelector{
		log:     log.With("component", "body.path-selector"),
		devices: make(map[string]*device.Device),
	}
}

// SelectInitialPath tries the three M5 initial paths in order, returning the
// first that completes a WireGuard handshake:
//
//  1. Direct WireGuard UDP to Core (default UDP bind)
//  2. Relay WireGuard UDP (default UDP bind via relay endpoint)
//  3. Relay WSS/TLS (BodyWSSBind)
//
// When a path starts successfully but the handshake times out, the tunnel is
// torn down and the next path is attempted.  The selector stops at the first
// path whose handshake completes.
func (ps *PathSelector) SelectInitialPath(ctx context.Context, bodyKey [32]byte, cfg PathSelectorConfig) *SelectResult {
	paths := []pathAttempt{
		{
			label:    "direct-wg",
			corePeer: cfg.CorePublicKey,
			newTunnel: func() (PathTunnel, error) {
				tun := newBodyTunnel(bodyKey, cfg.CorePublicKey, nil)
				tun.peerEndpoint = cfg.DirectEndpoint
				return tun, nil
			},
		},
		{
			label:    "relay-wg",
			corePeer: cfg.CorePublicKey,
			newTunnel: func() (PathTunnel, error) {
				tun := newBodyTunnel(bodyKey, cfg.CorePublicKey, nil)
				tun.peerEndpoint = cfg.RelayWGUDPEndpoint
				return tun, nil
			},
		},
		{
			label:    "relay-wss",
			corePeer: cfg.CorePublicKey,
			newTunnel: func() (PathTunnel, error) {
				bind := NewWSSBind(cfg.RelayWSSURL, cfg.RouteID, cfg.RouteCredential)
				if cfg.TLSConfig != nil {
					bind.SetTLSConfig(cfg.TLSConfig)
				}
				tun := newBodyTunnel(bodyKey, cfg.CorePublicKey, bind)
				tun.peerEndpoint = relay.RelayEndpointString(cfg.RouteID)
				return tun, nil
			},
		},
	}

	timeout := cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	result := &SelectResult{
		Devices: make(map[string]*device.Device),
	}

	for _, path := range paths {
		select {
		case <-ctx.Done():
			result.Err = ctx.Err()
			return result
		default:
		}

		ps.log.Info("trying path", "path", path.label)

		tun, err := path.newTunnel()
		if err != nil {
			ps.log.Info("path skipped: tunnel create failed", "path", path.label, "error", err)
			continue
		}

		if err := tun.Start(ctx); err != nil {
			ps.log.Info("path skipped: tunnel start failed", "path", path.label, "error", err)
			continue
		}

		// Store the device for identity verification.
		if bti, ok := tun.(*bodyTunnelI); ok && bti.dev != nil {
			result.Devices[path.label] = bti.dev
		}

		if err := tun.WaitHandshake(ctx, timeout); err != nil {
			_ = tun.Stop()
			ps.log.Info("path failed: handshake timeout", "path", path.label, "error", err)
			continue
		}

		ps.log.Info("path succeeded", "path", path.label)
		result.Path = path.label
		result.Tunnel = tun
		if bti, ok := tun.(*bodyTunnelI); ok {
			result.BoundBind = bti.bind
		}
		return result
	}

	result.Err = fmt.Errorf("body-path: all initial paths failed")
	ps.log.Warn("all initial paths failed")
	return result
}