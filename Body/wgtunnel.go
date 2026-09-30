// Package body provides WireGuard operations for the Doll Body, including a
// userspace WireGuard tunnel that connects to Core's WG endpoint via a
// pre-shared keypair and overlay address obtained during pairing (M2).
package body

import (
	"context"
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
)

// BodyTunnelConfig holds all parameters needed to start the Body-side
// WireGuard tunnel. Everything comes from the pairing protocol (M2).
type BodyTunnelConfig struct {
	// PrivateKey is the Body's Curve25519 private key (from Body/wgkey.go).
	PrivateKey [32]byte

	// CorePublicKey is Core's WireGuard public key.
	CorePublicKey [32]byte

	// OverlayAddress is the Body's assigned IPv6 ULA address.
	OverlayAddress netip.Addr

	// OverlayPrefix is the network's IPv6 ULA prefix.
	OverlayPrefix netip.Prefix

	// CoreEndpoint is Core's direct UDP reachable address ("ip:port").
	// Set explicitly during pairing, not derived from the HTTP URL.
	CoreEndpoint string

	// ListenPort is the local UDP port for WireGuard. 0 = auto.
	ListenPort int

	// MTU for the WireGuard overlay. 0 = DefaultMTU (1280).
	MTU int
}

// BodyTunnel manages a userspace WireGuard tunnel from the Body to Core.
// It provides the netstack.Net for local services that need to reach
// the overlay (e.g., connecting to Doll Link on Core's WS listener).
type BodyTunnel struct {
	tun     tun.Device
	net     *netstack.Net
	dev     *device.Device
	log     *slog.Logger
	cfg     BodyTunnelConfig
	started bool
}

// NewBodyTunnel creates a new Body-side WireGuard tunnel.
func NewBodyTunnel(cfg BodyTunnelConfig, log *slog.Logger) *BodyTunnel {
	if log == nil {
		log = slog.Default()
	}
	return &BodyTunnel{
		cfg: cfg,
		log: log.With("component", "body.wireguard"),
	}
}

// Start brings up the userspace WG tunnel, connecting to Core's endpoint.
func (bt *BodyTunnel) Start(ctx context.Context) error {
	if bt.started {
		return fmt.Errorf("body-wg: tunnel already started")
	}

	mtu := bt.cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}

	// Create the userspace TUN and netstack.
	tunDev, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{bt.cfg.OverlayAddress},
		nil, // no custom DNS resolvers
		mtu,
	)
	if err != nil {
		return fmt.Errorf("body-wg: create netstack: %w", err)
	}

	// WireGuard device bound to the userspace TUN.
	bind := conn.NewDefaultBind()
	l := device.NewLogger(device.LogLevelError, "body-wg: ")
	dev := device.NewDevice(tunDev, bind, l)

	// Set the private key via UAPI config.
	privHex := hex.EncodeToString(bt.cfg.PrivateKey[:])
	uapiCfg := fmt.Sprintf("private_key=%s\n", privHex)
	if bt.cfg.ListenPort > 0 {
		uapiCfg += fmt.Sprintf("listen_port=%d\n", bt.cfg.ListenPort)
	}
	if err := dev.IpcSet(uapiCfg); err != nil {
		dev.Close()
		return fmt.Errorf("body-wg: ipc set: %w", err)
	}

	// Configure the single peer: Core.
	pubHex := hex.EncodeToString(bt.cfg.CorePublicKey[:])
	peerCfg := fmt.Sprintf(
		"public_key=%s\nendpoint=%s\npersistent_keepalive_interval=25\nallowed_ip=%s\n",
		pubHex,
		bt.cfg.CoreEndpoint,
		bt.cfg.OverlayPrefix.String(),
	)
	if err := dev.IpcSet(peerCfg); err != nil {
		dev.Close()
		return fmt.Errorf("body-wg: ipc set peer: %w", err)
	}

	dev.Up()

	bt.tun = tunDev
	bt.net = tnet
	bt.dev = dev
	bt.started = true

	bt.log.Info("body wireguard tunnel started",
		"endpoint", bt.cfg.CoreEndpoint,
		"overlay", bt.cfg.OverlayAddress.String(),
	)
	return nil
}

// Stop shuts down the WireGuard tunnel.
func (bt *BodyTunnel) Stop() error {
	if !bt.started {
		return nil
	}
	bt.dev.Close()
	bt.started = false
	bt.log.Info("body wireguard tunnel stopped")
	return nil
}

// Netstack returns the userspace TCP/IP stack network handle.
func (bt *BodyTunnel) Netstack() *netstack.Net {
	return bt.net
}

// WaitHandshake blocks until the WireGuard handshake with Core completes,
// or the context is cancelled or timeout expires.
func (bt *BodyTunnel) WaitHandshake(ctx context.Context, timeout time.Duration) error {
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
			return fmt.Errorf("body-wg: handshake timeout (%v)", timeout)
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

// handshakeComplete checks whether the WG handshake with Core has completed
// by inspecting the IpcGet output.
func (bt *BodyTunnel) handshakeComplete() (bool, error) {
	out, err := bt.dev.IpcGet()
	if err != nil {
		return false, fmt.Errorf("body-wg: ipc get: %w", err)
	}
	// IpcGet output contains lines like "public_key=<key>" and
	// "last_handshake_time_nsec=<nsec>" for each peer.
	// A non-zero handshake time means the handshake completed.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "last_handshake_time_nsec=") {
			val := strings.TrimPrefix(trimmed, "last_handshake_time_nsec=")
			// If the value is "0", no handshake has occurred yet.
			if val != "0" && val != "" {
				return true, nil
			}
		}
	}
	return false, nil
}
