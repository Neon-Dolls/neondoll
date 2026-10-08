package body

import (
	"context"
	"log/slog"
)

// RecoverPath replaces a lost active path by closing the old tunnel and
// re-running bounded M5 path selection (SelectInitialPath) with the same
// identity configuration.
//
// The old tunnel is fully stopped — device, bind, and all goroutines —
// before selection begins, guaranteeing exactly one active authoritative
// path at all times.
//
// All identity is preserved because PathSelectorConfig is read-only:
// the same PrivateKey, CorePublicKey, OverlayAddress, and credentials
// are passed directly to SelectInitialPath. No re-pairing occurs.
//
// Returns the same shape as SelectInitialPath: a PathSelectionResult with
// Tunnel/Path/BoundBind on success, or an error via Err when all candidate
// paths are exhausted or ctx is cancelled.
func RecoverPath(
	ctx context.Context,
	oldTunnel *BodyTunnel,
	cfg PathSelectorConfig,
	log *slog.Logger,
) PathSelectionResult {
	if log == nil {
		log = slog.Default()
	}

	log.Info("recovering from path loss: stopping old tunnel")

	// Retire the old active path — this closes the WG device and its bind.
	oldTunnel.Stop()

	log.Info("re-running bounded path selection with preserved identity",
		"direct", cfg.DirectEndpoint,
		"relay-udp", cfg.RelayWGUDPEndpoint,
		"relay-wss", cfg.RelayWSSURL,
	)

	return SelectInitialPath(ctx, cfg, log)
}