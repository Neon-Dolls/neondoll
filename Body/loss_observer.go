// Package body defines the Body-side tunnel component of the NeonDoll system.
//
// The body package handles WireGuard-over-WebSocket tunneling on the Body side,
// providing path selection, loss detection, and tunnel lifecycle management.

package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ── Constants ──

// LossReason enumerates why a path was considered lost.
type LossReason string

const (
	// TransportFailure indicates the WG device itself died (IpcGet failed).
	TransportFailure LossReason = "TransportFailure"
	// WGLivenessLost indicates the WG handshake went stale beyond the timeout.
	WGLivenessLost LossReason = "WGLivenessLost"
)

// ── Config ──

// LossObserverConfig controls loss detection parameters.
type LossObserverConfig struct {
	// WGLivenessTimeout is the maximum allowed time since the last WireGuard
	// handshake before the path is considered dead. Must be > 0.
	WGLivenessTimeout time.Duration

	// PollInterval controls how often the observer checks for loss. Must be > 0.
	PollInterval time.Duration
}

// Validate returns an error if the config is invalid.
func (cfg LossObserverConfig) Validate() error {
	var errs []string
	if cfg.WGLivenessTimeout <= 0 {
		errs = append(errs, "WGLivenessTimeout must be > 0")
	}
	if cfg.PollInterval <= 0 {
		errs = append(errs, "PollInterval must be > 0")
	}
	if len(errs) > 0 {
		return fmt.Errorf("LossObserverConfig: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ── Event ──

// PathLossEvent describes a detected path loss event.
type PathLossEvent struct {
	// Reason categorises the type of loss.
	Reason LossReason

	// At records when the loss was detected.
	At time.Time

	// Detail provides diagnostic information.
	Detail string
}

// Error implements the error interface for convenience.
func (e PathLossEvent) Error() string {
	return fmt.Sprintf("[%s] path lost reason=%q at=%s detail=%s", e.Reason, e.Reason, e.At.Format(time.RFC3339Nano), e.Detail)
}

// ── StartLossObserver ──

// parseLastHandshakeUnix reads the last-handshake time from the BodyTunnel's
// WireGuard device and returns the Unix-nano timestamp. Returns zero and no
// error when the handshake line is absent (no peers).
func parseLastHandshakeUnix(bt *BodyTunnel) (int64, error) {
	ipcOut, err := bt.IpcGet()
	if err != nil {
		return 0, fmt.Errorf("parse handshake: %w", err)
	}
	return extractLastHandshakeNano(ipcOut)
}

// extractLastHandshakeNano parses the last_handshake_time_sec and
// last_handshake_time_nsec fields from an IpcGet-formatted string and
// returns them combined as a Unix-nano timestamp. Returns 0, nil if
// the fields are absent or zero (no handshake yet).
func extractLastHandshakeNano(ipcOut string) (int64, error) {
	var sec, nsec int64
	var found bool
	for _, line := range strings.Split(ipcOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			raw := strings.TrimPrefix(line, "last_handshake_time_sec=")
			if raw == "" {
				return 0, nil
			}
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse handshake sec: %w", err)
			}
			sec = v
			if v > 0 {
				found = true
			}
		}
		if strings.HasPrefix(line, "last_handshake_time_nsec=") {
			raw := strings.TrimPrefix(line, "last_handshake_time_nsec=")
			if raw == "" {
				raw = "0"
			}
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse handshake nsec: %w", err)
			}
			nsec = v
		}
	}
	if !found {
		return 0, nil
	}
	return sec*1_000_000_000 + nsec, nil
}

// LossObserverHandle holds the running state of a LossObserver goroutine.
type LossObserverHandle struct {
	Cancel context.CancelFunc
	Done   chan struct{}
}

// StartLossObserver begins monitoring an established BodyTunnel for path loss.
// The tunnel MUST have already completed a WireGuard handshake (the function
// fails with ErrNoHandshake if no handshake is recorded).
//
// The observer runs until:
//   - ctx is cancelled → clean exit (no event sent)
//   - the BodyTunnel is cleanly stopped via bt.Stop() → clean exit (no event sent)
//   - the last WireGuard handshake exceeds WGLivenessTimeout and the peer is
//     completely unreachable → WGLivenessLost event sent, then exits
//   - IpcGet succeeds but handshake-time parsing fails → logs the error and retries
//
// Returns a handle whose Done channel fires when the goroutine exits, or
// a non-nil error if the tunnel has no recorded handshake or the config
// is invalid.
func StartLossObserver(
	bt *BodyTunnel,
	ctx context.Context,
	cfg LossObserverConfig,
	onLoss chan PathLossEvent,
	log *slog.Logger,
) (*LossObserverHandle, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}

	// Verify initial handshake is present.
	nano, err := parseLastHandshakeUnix(bt)
	if err != nil {
		return nil, fmt.Errorf("initial handshake check: %w", err)
	}
	if nano <= 0 {
		return nil, errors.New("no handshake recorded — cannot start loss observer without an established path")
	}

	// Spawn the observer goroutine.
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer cancel()

		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Clean teardown — no event.
				return
			case <-bt.shutdown:
				// Clean lifecycle shutdown via bt.Stop() — no event.
				return

			case <-ticker.C:
				ipcOut, ipcErr := bt.IpcGet()
				if ipcErr != nil {
					// IpcGet should not fail from a running WG device (the device
					// reads from memory state). If it does, this could indicate a
					// transport failure — but since no independent production signal
					// exists in M6.1 to classify it, log and continue. M6.2+ may add
					// the Bind error wiring to produce this signal.
					log.Warn("loss-observer: IpcGet failed, retrying", "err", ipcErr)
					continue
				}

				handshakeNano, parseErr := extractLastHandshakeNano(ipcOut)
				if parseErr != nil {
					log.Warn("loss-observer: handshake parse error, retrying", "err", parseErr)
					continue
				}
				if handshakeNano <= 0 {
					// No handshake yet — skip this poll.
					continue
				}

				elapsed := time.Since(time.Unix(0, handshakeNano))
				if elapsed > cfg.WGLivenessTimeout {
					select {
					case <-bt.shutdown:
						return
					case onLoss <- PathLossEvent{
						Reason: WGLivenessLost,
						At:     time.Now(),
						Detail: fmt.Sprintf(
							"last handshake %.0fs ago (timeout %s)",
							elapsed.Seconds(),
							cfg.WGLivenessTimeout,
						),
					}:
					case <-ctx.Done():
						return
					}
					return
				}
			}
		}
	}()

	return &LossObserverHandle{Cancel: cancel, Done: done}, nil
}