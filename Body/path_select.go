// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

const (
	// defaultAttemptTimeout is the default max time to wait for a
	// WireGuard handshake on each path attempt.
	defaultAttemptTimeout = 5 * time.Second
)

// PathSelectorConfig contains the endpoints and credentials for attempting
// WireGuard paths at Body startup.  All paths share the same WG identity and
// overlay — the selector only varies the transport endpoint and bind type.
type PathSelectorConfig struct {
	// PrivateKey is the Body's WireGuard static private key (same for all paths).
	PrivateKey [32]byte

	// CorePublicKey is the Core's WireGuard static public key (same for all paths).
	CorePublicKey [32]byte

	// OverlayAddress is the Body's overlay address.
	OverlayAddress netip.Addr
	// OverlayPrefix is the Body's overlay prefix.
	OverlayPrefix netip.Prefix

	// DirectEndpoint is Core's direct WireGuard UDP endpoint ("ip:port").
	DirectEndpoint string

	// RelayWGUDPEndpoint is the Relay's WireGuard UDP endpoint ("ip:port").
	RelayWGUDPEndpoint string

	// RelayWSSURL is the Relay's /body WebSocket endpoint URL.
	RelayWSSURL string

	// RouteID identifies the relay route (for relay paths).
	RouteID relay.RouteID

	// RouteCredential authenticates the relay route (for relay paths).
	RouteCredential string

	// TLSConfig optionally overrides the default TLS configuration for
	// the relay WSS bind, e.g. to accept self-signed certificates.
	TLSConfig *tls.Config

	// PerAttemptTimeout limits how long each path attempt waits for a
	// WireGuard handshake.  Zero means 5 seconds.
	PerAttemptTimeout time.Duration
}

// PathSelectionError values.
var (
	ErrAllPathsFailed = errors.New("path selection: all paths failed")
	ErrPathCancelled  = errors.New("path selection: operation cancelled")
)

// PathSelectionResult carries the outcome of SelectInitialPath.
// On success Tunnel is set with the active, handshake-complete tunnel.
// On failure Err is set and Tunnel is nil.
type PathSelectionResult struct {
	// Tunnel is the active tunnel after a successful handshake.
	Tunnel *BodyTunnel
	// Path is a label — "direct", "relay-udp", "relay-wss".
	Path string
	// Err is set when all paths failed.
	Err error
	// BoundBind holds ownership of the conn.Bind for non-default transports
	// (currently BodyWSSBind).  The callee MUST Close() it if Tunnel is
	// returned; if Err is returned, BoundBind has already been closed by
	// SelectInitialPath.
	BoundBind conn.Bind
}

// ── Public entry point ──────────────────────────────────────────────────────

// SelectInitialPath attempts paths in bounded order: direct WG UDP → relay WG
// UDP → relay WSS.  Each path is tried with timeout, and a path wins only
// after a real WireGuard handshake completes.  Failed attempts are fully torn
// down (TUN, device, bind) before the next path is tried.
//
// If the parent context (ctx) is cancelled during any attempt, selection stops
// immediately and returns ErrPathCancelled — subsequent paths are not tried.
//
// cfg.PerAttemptTimeout defaults to 5 seconds when zero.
func SelectInitialPath(ctx context.Context, cfg PathSelectorConfig, log *slog.Logger) PathSelectionResult {
	timeout := cfg.PerAttemptTimeout
	if timeout <= 0 {
		timeout = defaultAttemptTimeout
	}

	log.Info("path selection: starting", "timeout", timeout)

	// Track which paths failed and why.
	var directErr error
	var relayUDPErr error
	var wssErr error

	// ── 1. Direct WG UDP ──────────────────────────────────────────────────────
	if cfg.DirectEndpoint != "" {
		log := log.With("attempt", "direct")
		tunnelCfg := bodyTunnelConfigFromSelector(cfg, cfg.DirectEndpoint, nil)
		tunnel := NewBodyTunnel(tunnelCfg, log)
		if err := tunnel.Start(ctx); err != nil {
			log.Warn("direct: start failed", "error", err)
			directErr = err
		} else if err := tunnel.WaitHandshake(ctx, timeout); err == nil {
			log.Info("path selected: direct UDP")
			return PathSelectionResult{Tunnel: tunnel, Path: "direct"}
		} else {
			log.Info("direct: handshake timeout, trying next",
				"error", err)
			tunnel.Stop()
			directErr = err
		}
		// If the parent context was cancelled, stop selection immediately.
		if cerr := ctx.Err(); cerr != nil {
			return PathSelectionResult{Err: ErrPathCancelled}
		}
	}

	// ── 2. Relay WG UDP ──────────────────────────────────────────────────────
	if cfg.RelayWGUDPEndpoint != "" {
		log := log.With("attempt", "relay-udp")
		tunnelCfg := bodyTunnelConfigFromSelector(cfg, cfg.RelayWGUDPEndpoint, nil)
		tunnel := NewBodyTunnel(tunnelCfg, log)
		if err := tunnel.Start(ctx); err != nil {
			log.Warn("relay-udp: start failed", "error", err)
			relayUDPErr = err
		} else if err := tunnel.WaitHandshake(ctx, timeout); err == nil {
			log.Info("path selected: relay UDP")
			return PathSelectionResult{Tunnel: tunnel, Path: "relay-udp"}
		} else {
			log.Info("relay-udp: handshake timeout, trying next",
				"error", err)
			tunnel.Stop()
			relayUDPErr = err
		}
		if cerr := ctx.Err(); cerr != nil {
			return PathSelectionResult{Err: ErrPathCancelled}
		}
	}

	// ── 3. Relay WSS ─────────────────────────────────────────────────────────
	if cfg.RelayWSSURL != "" {
		log := log.With("attempt", "relay-wss")
		wssBind := NewWSSBind(cfg.RelayWSSURL, cfg.RouteID, cfg.RouteCredential)
		if cfg.TLSConfig != nil {
			wssBind.SetTLSConfig(cfg.TLSConfig)
		}
		peerEndpoint := relay.RelayEndpointString(cfg.RouteID)
		tunnelCfg := bodyTunnelConfigFromSelector(cfg, peerEndpoint, wssBind)
		tunnel := NewBodyTunnel(tunnelCfg, log)
		if err := tunnel.Start(ctx); err != nil {
			wssBind.Close()
			wssErr = err
		} else {
			hsErr := tunnel.WaitHandshake(ctx, timeout)
			if hsErr == nil {
				log.Info("path selected: relay WSS")
				return PathSelectionResult{
					Tunnel: tunnel, Path: "relay-wss", BoundBind: wssBind,
				}
			}
			log.Info("relay-wss: handshake timeout", "error", hsErr)
			tunnel.Stop()
			wssBind.Close()
			wssErr = hsErr
		}
		if cerr := ctx.Err(); cerr != nil {
			return PathSelectionResult{Err: ErrPathCancelled}
		}
	}

	// ── All configured paths failed ──────────────────────────────────────────
	var detail string
	if directErr != nil {
		if detail != "" {
			detail += ", "
		}
		detail += fmt.Sprintf("direct (%v)", directErr)
	}
	if relayUDPErr != nil {
		if detail != "" {
			detail += ", "
		}
		detail += fmt.Sprintf("relay-udp (%v)", relayUDPErr)
	}
	if wssErr != nil {
		if detail != "" {
			detail += ", "
		}
		detail += fmt.Sprintf("relay-wss (%v)", wssErr)
	}
	return PathSelectionResult{
		Err: fmt.Errorf("%w: %s", ErrAllPathsFailed, detail),
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// bodyTunnelConfigFromSelector builds a BodyTunnelConfig from the shared
// PathSelectorConfig, overriding the peer endpoint and optionally the bind.
func bodyTunnelConfigFromSelector(cfg PathSelectorConfig, peerEndpoint string, bind conn.Bind) BodyTunnelConfig {
	return BodyTunnelConfig{
		PrivateKey:     cfg.PrivateKey,
		CorePublicKey:  cfg.CorePublicKey,
		OverlayAddress: cfg.OverlayAddress,
		OverlayPrefix:  cfg.OverlayPrefix,
		CoreEndpoint:   peerEndpoint,
		ListenPort:     0,
		MTU:            0,
		Bind:           bind,
	}
}
