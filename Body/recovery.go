package body

import (
	"context"
	"fmt"
	"log/slog"
)

// RecoverPath attempts to establish a new path after the current one is lost.
//
// It stops the old tunnel, then reuses SelectInitialPath (the M5 path selector)
// to find the next viable path in priority order (direct → relay-udp → relay-wss).
// The WG identity (private key, public key), overlay address, prefix, and peer
// configuration from cfg are preserved — no re-pairing occurs.
//
// Integration with M6.1 (Active Path-Loss Detection):
// The caller should invoke RecoverPath when it receives a WGLivenessLost event
// from a LossObserver. The observer's onLoss callback is the integration point:
//
//	func() {
//	    result := RecoverPath(ctx, currentTunnel, cfg, log)
//	    currentTunnel = result.Tunnel
//	}
//
// This ensures exactly one active authoritative path at all times: the old
// tunnel is stopped before the new path is selected.
//
// Bounded recovery guarantees:
//   - Old tunnel and its bind are fully retired before selection begins.
//   - If all candidate paths fail, an error is returned and the system has
//     no active path — the caller must decide how to proceed.
//   - Context cancellation is honoured and returns immediately.
//
// No M6.3 epochs, stale-attempt protection, background probing,
// preferred-path return, or migration is implemented here.
func RecoverPath(ctx context.Context, oldTunnel *BodyTunnel, cfg PathSelectorConfig, log *slog.Logger) PathSelectionResult {
	log.Info("recovering path after loss",
		"direct", cfg.DirectEndpoint,
		"relay-udp", cfg.RelayWGUDPEndpoint,
		"relay-wss", cfg.RelayWSSURL,
	)

	// Retire the old tunnel first — must succeed to preserve
	// the one-authoritative-path invariant.
	if err := oldTunnel.Stop(); err != nil {
		return PathSelectionResult{
			Err: fmt.Errorf("recover path: old tunnel teardown failed: %w", err),
		}
	}

	return SelectInitialPath(ctx, cfg, log)
}
