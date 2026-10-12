// SPDX-License-Identifier: AGPL-3.0-only

package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// Path labels used for identifying candidate paths in path discovery and
// recovery.  These are the only three paths the system knows about.
const (
	PathDirect   = "direct"
	PathRelayUDP = "relay-udp"
	PathRelayWSS = "relay-wss"
)

// pathPreferenceOrder defines the probe order from most to least preferred.
var pathPreferenceOrder = []string{PathDirect, PathRelayUDP, PathRelayWSS}

// ProbeResult holds the outcome of probing a single candidate path.
//
// When HandshakeOK is true, the temporary tunnel and (for WSS probes) the WSS
// bind are still alive and ready for the caller to use for migration.  The
// caller takes ownership and MUST call Tunnel.Stop() and (if non-nil)
// WSSBind.Close().
//
// When HandshakeOK is false, Tunnel and WSSBind are always nil — the probe
// was torn down completely.
type ProbeResult struct {
	// Path is the candidate path label ("direct", "relay-udp", "relay-wss").
	Path string
	// HandshakeOK is true when a real WireGuard handshake completed within
	// the probe timeout.  When true the Tunnel (and WSSBind) are valid and
	// must be cleaned up by the caller.
	HandshakeOK bool
	// Duration is the wall-clock time the probe took.
	Duration time.Duration
	// Error is non-nil when the probe failed (timeout, start failure,
	// cancellation, or the path was not configured).
	Error error

	// Tunnel is the probed tunnel, valid only when HandshakeOK is true.
	// The caller takes ownership.
	Tunnel *BodyTunnel
	// WSSBind is set only for WSS-probe paths, valid only when HandshakeOK
	// is true.  The caller takes ownership.
	WSSBind *BodyWSSBind
	// HandshakeAt records when the WireGuard handshake completed.
	HandshakeAt time.Time
}

// defaultAttemptTimeout is shared with path_select.go and defined there.
// Re-declared here for visibility but the value is the same package-level var.

// ProbePath attempts a real WireGuard handshake over the given candidate path.
//
// It creates a temporary, isolated tunnel sharing the same WG identity (same
// keypair and overlay) on an OS-assigned ephemeral port, attempts a handshake
// within the configured timeout.
//
//	On success: the tunnel (and WSS bind for WSS paths) are returned running
//	in ProbeResult for the caller to use for migration.  Caller MUST clean up
//	by calling Tunnel.Stop() and (if non-nil) WSSBind.Close().
//
//	On failure: everything is torn down.  Tunnel and WSSBind are nil in the
//	returned ProbeResult.
//
// The active tunnel is never touched.  Using the same WG identity on a
// separate port is safe because:
//
//   - WireGuard's handshake transition window keeps the old session keys
//     valid during reinitiation, so the active session continues to
//     decrypt and send packets throughout the brief probe.
//   - On the Relay, WSS and UDP transports create completely separate
//     connection IDs, so relay return-path routing is not affected.
//
// ProbePath respects context cancellation: if ctx is cancelled during the
// probe, the temporary tunnel is stopped and all resources are released.
func ProbePath(ctx context.Context, cfg PathSelectorConfig, path string, log *slog.Logger) ProbeResult {
	start := time.Now()
	timeout := cfg.PerAttemptTimeout
	if timeout <= 0 {
		timeout = defaultAttemptTimeout
	}

	log = log.With("probe", path, "timeout", timeout)
	log.Info("probing candidate path")

	// Build the tunnel config for the candidate path.
	var wssBind *BodyWSSBind // non-nil only for WSS paths; closed on failure

	var tunnelCfg BodyTunnelConfig
	switch path {
	case PathDirect:
		if cfg.DirectEndpoint == "" {
			return ProbeResult{
				Path: path, HandshakeOK: false, Duration: time.Since(start),
				Error: errors.New("direct endpoint not configured"),
			}
		}
		tunnelCfg = bodyTunnelConfigFromSelector(cfg, cfg.DirectEndpoint, nil)

	case PathRelayUDP:
		if cfg.RelayWGUDPEndpoint == "" {
			return ProbeResult{
				Path: path, HandshakeOK: false, Duration: time.Since(start),
				Error: errors.New("relay UDP endpoint not configured"),
			}
		}
		tunnelCfg = bodyTunnelConfigFromSelector(cfg, cfg.RelayWGUDPEndpoint, nil)

	case PathRelayWSS:
		if cfg.RelayWSSURL == "" {
			return ProbeResult{
				Path: path, HandshakeOK: false, Duration: time.Since(start),
				Error: errors.New("relay WS URL not configured"),
			}
		}
		wssBind = NewWSSBind(cfg.RelayWSSURL, cfg.RouteID, cfg.RouteCredential)
		if cfg.TLSConfig != nil {
			wssBind.SetTLSConfig(cfg.TLSConfig)
		}
		peerEndpoint := relay.RelayEndpointString(cfg.RouteID)
		tunnelCfg = bodyTunnelConfigFromSelector(cfg, peerEndpoint, wssBind)

	default:
		return ProbeResult{
			Path: path, HandshakeOK: false, Duration: time.Since(start),
			Error: fmt.Errorf("unknown probe path: %s", path),
		}
	}

	// Create the probe tunnel.
	tunnel := NewBodyTunnel(tunnelCfg, log)

	// Start the tunnel with the parent context (not probeCtx) so that a
	// successful probe returns a running tunnel the caller can use.  Only
	// the handshake wait uses the bounded probeCtx.
	startErr := tunnel.Start(ctx)
	if startErr != nil {
		// Tunnel didn't start. Clean up WSS bind if we had one.
		if wssBind != nil {
			wssBind.Close()
		}
		elapsed := time.Since(start)
		log.Warn("probe: tunnel start failed", "error", startErr, "elapsed", elapsed)
		return ProbeResult{
			Path:        path,
			HandshakeOK: false,
			Duration:    elapsed,
			Error:       fmt.Errorf("probe tunnel start: %w", startErr),
		}
	}

	// Wait for the WireGuard handshake within the probe timeout.
	// We use a sub-context so we don't block forever if the parent
	// context is very long-lived.
	probeCtx, probeCancel := context.WithTimeout(ctx, timeout)
	defer probeCancel()

	hsErr := tunnel.WaitHandshake(probeCtx, timeout)
	elapsed := time.Since(start)

	if hsErr == nil {
		now := time.Now()
		log.Info("probe: handshake completed", "elapsed", elapsed)
		return ProbeResult{
			Path:        path,
			HandshakeOK: true,
			Duration:    elapsed,
			Error:       nil,
			Tunnel:      tunnel,
			WSSBind:     wssBind,
			HandshakeAt: now,
		}
	}

	// Handshake failed.  Tear down the probe tunnel.
	tunnel.Stop()
	if wssBind != nil {
		wssBind.Close()
	}

	log.Info("probe: no handshake", "error", hsErr, "elapsed", elapsed)
	return ProbeResult{
		Path:        path,
		HandshakeOK: false,
		Duration:    elapsed,
		Error:       hsErr,
	}
}

// ProbeConfig controls the behaviour of ProbeBetter.
type ProbeConfig struct {
	// PerAttemptTimeout limits each individual probe attempt.  Zero means
	// PathSelectorConfig.PerAttemptTimeout, falling back to 5 seconds.
	PerAttemptTimeout time.Duration
	// MinBackoff is the initial delay between probe attempts.  Zero = 1s.
	MinBackoff time.Duration
	// MaxBackoff caps the exponential backoff.  Zero = 10s.
	MaxBackoff time.Duration
	// BackoffFactor is the exponential multiplier.  Zero or negative = 2.0.
	BackoffFactor float64
}

func defaultProbeConfig() ProbeConfig {
	return ProbeConfig{
		PerAttemptTimeout: 0, // use PathSelectorConfig's or fallback
		MinBackoff:        1 * time.Second,
		MaxBackoff:        10 * time.Second,
		BackoffFactor:     2.0,
	}
}

// effectiveBackoff returns the next backoff duration given the current delay.
// It starts at MinBackoff and grows by BackoffFactor, then caps at MaxBackoff.
func (cfg ProbeConfig) effectiveBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return cfg.MinBackoff
	}
	next := time.Duration(float64(current) * cfg.BackoffFactor)
	if next > cfg.MaxBackoff {
		return cfg.MaxBackoff
	}
	return next
}

// resolveProbeTimeout returns the effective probe timeout, preferring
// ProbeConfig.PerAttemptTimeout over PathSelectorConfig.PerAttemptTimeout,
// falling back to the package default.
func resolveProbeTimeout(pCfg ProbeConfig, sCfg PathSelectorConfig) time.Duration {
	if pCfg.PerAttemptTimeout > 0 {
		return pCfg.PerAttemptTimeout
	}
	if sCfg.PerAttemptTimeout > 0 {
		return sCfg.PerAttemptTimeout
	}
	return defaultAttemptTimeout
}

// candidatesBetterThan returns paths more preferred than the given path, in
// order of preference.  Returns nil if the given path is already the best
// (direct) or unrecognised.
func candidatesBetterThan(currentPath string) []string {
	for i, p := range pathPreferenceOrder {
		if p == currentPath {
			// Return all paths more preferred than current.
			return pathPreferenceOrder[:i]
		}
	}
	// Unknown path — treat as no known candidate.
	return nil
}

// ProbeBetter tries candidate paths that are better than currentPath, in
// preference order (direct > relay-udp > relay-wss).
//
// For each candidate it performs a real WireGuard handshake via ProbePath.
// If a candidate succeeds, ProbeBetter returns a *ProbeResult with the
// running tunnel (and WSS bind, if applicable) that the caller can use
// for migration.  The caller takes ownership of the tunnel and must call
// Tunnel.Stop() and (if non-nil) WSSBind.Close().
//
// ProbeBetter behaviour:
//   - If the candidate succeeds (real WireGuard handshake), it is returned
//     immediately as *ProbeResult with HandshakeOK=true and Tunnel set.
//   - If the candidate fails (handshake timeout, start failure, etc.), the
//     probe is cleaned up, a backoff delay is applied, and probing continues
//     to the next candidate.
//   - Failed, cancelled and superseded candidates are cleaned up immediately.
//   - Cancel the ctx at any point — in-progress probes are stopped, backoff
//     is interrupted, and any partial state is released.
//
// ProbeBetter returns nil when no better path is available (current path is
// already the best or unknown), all candidates failed, or ctx was cancelled.
//
// The active tunnel is never modified.  The caller is responsible for using
// the returned candidate with migration logic (future work).
func ProbeBetter(
	ctx context.Context,
	cfg PathSelectorConfig,
	currentPath string,
	probeCfg ProbeConfig,
	log *slog.Logger,
) *ProbeResult {
	// On the best path — nothing to probe.
	if candidates := candidatesBetterThan(currentPath); len(candidates) == 0 {
		log.Info("path prober: on best path, nothing to probe",
			"current", currentPath)
		return nil
	}

	// Ensure sensible defaults for probe config fields not set.
	if probeCfg.MinBackoff <= 0 {
		probeCfg.MinBackoff = 1 * time.Second
	}
	if probeCfg.MaxBackoff <= 0 {
		probeCfg.MaxBackoff = 10 * time.Second
	}
	if probeCfg.BackoffFactor <= 0 {
		probeCfg.BackoffFactor = 2.0
	}
	timeout := resolveProbeTimeout(probeCfg, cfg)

	candidates := candidatesBetterThan(currentPath)

	log.Info("path prober: seeking better path",
		"current", currentPath,
		"candidates", candidates,
		"timeout", timeout,
		"min_backoff", probeCfg.MinBackoff,
		"max_backoff", probeCfg.MaxBackoff)

	backoff := time.Duration(0)

	for i, candidate := range candidates {
		// Check cancellation before each probe attempt.
		if ctx.Err() != nil {
			log.Info("path prober: cancelled",
				"candidate", candidate, "attempt", i+1)
			return nil
		}

		// Apply backoff before probing (skip on first attempt).
		if backoff > 0 {
			log.Debug("path prober: backoff",
				"duration", backoff, "candidate", candidate)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				log.Info("path prober: cancelled during backoff",
					"candidate", candidate)
				return nil
			}
		}

		result := ProbePath(ctx, cfg, candidate, log)

		if result.HandshakeOK {
			log.Info("path prober: found viable candidate",
				"candidate", candidate,
				"duration", result.Duration)
			return &result
		}

		// Candidate failed.  Compute backoff for the next attempt.
		log.Info("path prober: candidate failed",
			"candidate", candidate, "error", result.Error)
		backoff = probeCfg.effectiveBackoff(backoff)
	}

	log.Info("path prober: no viable better path found",
		"candidates_tried", len(candidates))
	return nil
}
