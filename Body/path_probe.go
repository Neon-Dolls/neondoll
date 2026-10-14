// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

// M6.4 — Non-disruptive path reachability discovery.
//
// ReachProbe tests whether a relay's probe endpoint is reachable and
// authenticates the response using route credentials.  No WG device,
// tunnel, or key material is involved — the probe is a lightweight
// HMAC-authenticated UDP datagram exchange.
//
// ProbeScheduler bounds the frequency of probe cycles, implements
// exponential backoff on failure, and stamps results with RecoveryEpoch for
// stale-result rejection.

// ─── ReachProbe ────────────────────────────────────────────────────────────

// ReachProbeConfig configures a single reachability probe attempt.
type ReachProbeConfig struct {
	// Target is the relay's probe endpoint (host:port).
	Target string

	// RouteID is the relay route to probe.
	RouteID uint64

	// Credential is the HMAC key — the route's credential Token bytes.
	Credential string

	// Timeout is how long to wait for a probe response.
	Timeout time.Duration
}

// ReachProbeResult captures the outcome of a single probe attempt.
type ReachProbeResult struct {
	Reachable bool
	Duration  time.Duration
	Error     error
}

// ReachProbe sends a single authenticated probe to a relay's probe
// endpoint and waits for a verified response.  It creates a short-lived
// UDP socket, sends one datagram, and reads exactly one response.
//
// The probe uses the same binary HMAC protocol as ProbeListener:
//
//	Request:  magic(4) + routeID(8) + nonce(8) + HMAC(32)  = 52 bytes
//	Response: magic(4) + routeID(8) + nonce(8) + HMAC(32)  = 52 bytes
//
// HMAC-SHA256 keyed on the route credential Token.
func ReachProbe(ctx context.Context, cfg ReachProbeConfig) ReachProbeResult {
	start := time.Now()

	conn, err := net.DialTimeout("udp", cfg.Target, cfg.Timeout)
	if err != nil {
		return ReachProbeResult{Error: fmt.Errorf("reach probe: dial %s: %w", cfg.Target, err), Duration: time.Since(start)}
	}

	// Deadline for the entire exchange — honour whichever is shorter:
	// the caller's context deadline or the per-probe Timeout.
	deadline := start.Add(cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	type exchangeResult struct {
		reachable bool
		err       error
	}

	resultCh := make(chan exchangeResult, 1)

	go func() {
		defer conn.Close()

		nonce := make([]byte, 8)
		if _, err := rand.Read(nonce); err != nil {
			resultCh <- exchangeResult{err: fmt.Errorf("reach probe: nonce: %w", err)}
			return
		}

		req := encodeProbeRequest(cfg.RouteID, nonce, cfg.Credential)
		if _, err := conn.Write(req); err != nil {
			resultCh <- exchangeResult{err: fmt.Errorf("reach probe: write: %w", err)}
			return
		}

		resp := make([]byte, 52)
		n, err := conn.Read(resp)
		if err != nil {
			resultCh <- exchangeResult{err: fmt.Errorf("reach probe: read: %w", err)}
			return
		}

		if err := verifyProbeResponse(resp[:n], cfg.RouteID, nonce, cfg.Credential); err != nil {
			resultCh <- exchangeResult{err: fmt.Errorf("reach probe: verify: %w", err)}
			return
		}

		resultCh <- exchangeResult{reachable: true}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			return ReachProbeResult{Error: res.err, Duration: time.Since(start)}
		}
		return ReachProbeResult{Reachable: res.reachable, Duration: time.Since(start)}
	case <-ctx.Done():
		conn.Close()
		return ReachProbeResult{Error: fmt.Errorf("reach probe: cancelled: %w", ctx.Err()), Duration: time.Since(start)}
	}
}

func encodeProbeRequest(routeID uint64, nonce []byte, credential string) []byte {
	buf := make([]byte, 52)
	copy(buf[0:4], []byte("NLPR"))
	binary.LittleEndian.PutUint64(buf[4:12], routeID)
	copy(buf[12:20], nonce)
	mac := computeProbeHMAC("NLPR", routeID, nonce, credential)
	copy(buf[20:52], mac)
	return buf
}

func verifyProbeResponse(data []byte, routeID uint64, nonce []byte, credential string) error {
	if len(data) < 52 {
		return fmt.Errorf("response too short: %d < 52", len(data))
	}
	if string(data[0:4]) != "NLPS" {
		return errors.New("bad response magic")
	}
	if binary.LittleEndian.Uint64(data[4:12]) != routeID {
		return errors.New("route ID mismatch")
	}
	if string(data[12:20]) != string(nonce) {
		return errors.New("nonce mismatch")
	}
	expected := computeProbeHMAC("NLPS", routeID, nonce, credential)
	if !hmac.Equal(data[20:52], expected) {
		return errors.New("HMAC mismatch")
	}
	return nil
}

func computeProbeHMAC(magic string, routeID uint64, nonce []byte, credential string) []byte {
	m := hmac.New(sha256.New, []byte(credential))
	m.Write([]byte(magic))
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], routeID)
	m.Write(id[:])
	m.Write(nonce)
	return m.Sum(nil)
}

// ─── ProbeScheduler ────────────────────────────────────────────────────────

// ProbeScheduler bounds the frequency of probe cycles and provides
// exponential backoff on failure.  TryAcquireSlot reserves a slot for a
// single concurrent probe; RecordResult and RecordCancelled release it.
// It is safe for concurrent use.
type ProbeScheduler struct {
	Config ProbeSchedulerConfig

	// epoch is the authoritative RecoveryEpoch lifecycle owner.  Stale()
	// checks results against this epoch rather than maintaining a separate
	// generation counter that could drift.
	epoch *RecoveryEpoch

	mu        sync.Mutex
	lastProbe time.Time
	inFlight  bool // true while a probe slot is reserved
	backoff    time.Duration
	attempts   int
	exhaustedAt time.Time // set when attempts >= MaxAttempts, cleared on cooldown expiry
}

// ProbeSchedulerConfig configures probe scheduling behaviour.
type ProbeSchedulerConfig struct {
	// MinInterval is the minimum time between consecutive probe cycles.
	// The scheduler will not allow more than one probe every MinInterval.
	MinInterval time.Duration

	// MaxBackoff caps the exponential backoff duration.
	MaxBackoff time.Duration

	// MaxAttempts is the maximum consecutive failed attempts before the
	// scheduler pauses discovery for MaxAttemptsCooldown before resuming.
	// Zero means no limit.
	MaxAttempts int

	// MaxAttemptsCooldown is how long discovery is paused after exhausting
	// MaxAttempts before resuming. Zero means 10 × MaxBackoff.
	MaxAttemptsCooldown time.Duration

	// InitialBackoff is the backoff duration after the first failure.
	InitialBackoff time.Duration
}

// DefaultProbeSchedulerConfig returns a sensible default configuration.
func DefaultProbeSchedulerConfig() ProbeSchedulerConfig {
	return ProbeSchedulerConfig{
		MinInterval:    30 * time.Second,
		MaxBackoff:     10 * time.Minute,
		MaxAttempts:    10,
		InitialBackoff: 5 * time.Second,
	}
}

// NewProbeScheduler creates a scheduler with the given config and epoch.
// The epoch pointer must be non-nil; it is the authoritative RecoveryEpoch
// lifecycle owner used by Stale() for stale-result rejection.
func NewProbeScheduler(cfg ProbeSchedulerConfig, epoch *RecoveryEpoch) *ProbeScheduler {
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = 30 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 10 * time.Minute
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 5 * time.Second
	}
	return &ProbeScheduler{
		Config:  cfg,
		epoch:   epoch,
		backoff: cfg.InitialBackoff,
	}
}

// TryAcquireSlot atomically checks whether a new probe cycle is permitted
// AND reserves the slot if so.  Unlike the old ShouldProbe (which was
// read-only), concurrent callers cannot both observe a true result and
// start overlapping cycles — one of them will find the slot taken.
//
// The check honours: in-flight reservation, MinInterval, exponential
// backoff, and MaxAttempts.
func (s *ProbeScheduler) TryAcquireSlot(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reject if a probe is already in-flight — only one concurrent probe
	// is allowed regardless of how much time has elapsed since the last one.
	if s.inFlight {
		return false
	}

	// Enforce minimum interval between probe cycles.
	if !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < s.Config.MinInterval {
		return false
	}

	// Enforce exponential backoff.
	if !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < s.backoff {
		return false
	}

	// Enforce max attempts with cooldown.
	if s.Config.MaxAttempts > 0 && s.attempts >= s.Config.MaxAttempts {
		if s.exhaustedAt.IsZero() || now.Sub(s.exhaustedAt) < s.cooldownDuration() {
			return false
		}
		// Cooldown expired — reset and allow probing.
		s.attempts = 0
		s.backoff = s.Config.InitialBackoff
		s.exhaustedAt = time.Time{}
	}

	// Reserve the slot — mark as in-flight so no other caller can sneak in.
	s.lastProbe = now
	s.inFlight = true
	return true
}

// BackoffDuration returns how long the caller should wait before the next
// probe cycle.  It accounts for in-flight state, MinInterval, and
// exponential backoff, consistent with TryAcquireSlot's rejection policy.
//
// When a probe is in-flight, BackoffDuration returns max(MinInterval,
// backoff) — the minimum wall time that must elapse after the current
// reservation before the caller should even ask for a new slot.
func (s *ProbeScheduler) BackoffDuration(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	// In-flight probe blocks unconditionally — no slot available until
	// RecordResult or RecordCancelled releases it.
	if s.inFlight {
		return time.Duration(math.Max(
			float64(s.Config.MinInterval),
			float64(s.backoff),
		))
	}

	// Never probed and not in-flight → slot is available immediately.
	if s.lastProbe.IsZero() {
		return 0
	}

	elapsed := now.Sub(s.lastProbe)

	var remainingMinInterval time.Duration
	if elapsed < s.Config.MinInterval {
		remainingMinInterval = s.Config.MinInterval - elapsed
	}

	var remainingBackoff time.Duration
	if elapsed < s.backoff {
		remainingBackoff = s.backoff - elapsed
	}

	// Return the max of the blocking durations — all must clear
	// before TryAcquireSlot would accept.
	maxBlock := remainingMinInterval
	if remainingBackoff > maxBlock {
		maxBlock = remainingBackoff
	}

	// Also account for MaxAttempts cooldown.
	if s.Config.MaxAttempts > 0 && s.attempts >= s.Config.MaxAttempts && !s.exhaustedAt.IsZero() {
		cd := s.cooldownDuration()
		remainingExhausted := cd - now.Sub(s.exhaustedAt)
		if remainingExhausted < 0 {
			remainingExhausted = 0
		}
		if remainingExhausted > maxBlock {
			maxBlock = remainingExhausted
		}
	}

	return maxBlock
}

// cooldownDuration returns how long to pause after exhausting MaxAttempts.
// Defaults to 10 × MaxBackoff when MaxAttemptsCooldown is zero.
func (s *ProbeScheduler) cooldownDuration() time.Duration {
	if s.Config.MaxAttemptsCooldown > 0 {
		return s.Config.MaxAttemptsCooldown
	}
	return 10 * s.Config.MaxBackoff
}

// RecordResult feeds back a probe result into the scheduler's backoff and
// attempt tracking.  reachable=true resets the backoff; reachable=false
// increases it exponentially up to MaxBackoff.
// Releases the in-flight slot so TryAcquireSlot may succeed again.
func (s *ProbeScheduler) RecordResult(reachable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastProbe = time.Now()
	s.inFlight = false // release slot

	if reachable {
		// Successful probe resets backoff, attempts, and exhaustion.
		s.backoff = 0
		s.attempts = 0
		s.exhaustedAt = time.Time{}
	} else {
		s.attempts++
		// Start cooldown if we just exhausted MaxAttempts.
		if s.Config.MaxAttempts > 0 && s.attempts >= s.Config.MaxAttempts {
			if s.exhaustedAt.IsZero() {
				s.exhaustedAt = time.Now()
			}
		} else {
			s.backoff = time.Duration(math.Min(
				float64(s.Config.InitialBackoff)*math.Pow(2, float64(s.attempts-1)),
				float64(s.Config.MaxBackoff),
			))
		}
	}
}

// RecordCancelled resets the scheduler so the next TryAcquireSlot call will
// allow probing (after the MinInterval).  A cancelled probe didn't fail, so
// no backoff penalty is applied.  The lastProbe timestamp is updated to now
// so that MinInterval is enforced from the cancellation point.
// Releases the in-flight slot.
func (s *ProbeScheduler) RecordCancelled() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.backoff = 0
	s.attempts = 0
	s.inFlight = false // release slot
	s.exhaustedAt = time.Time{} // clear exhaustion on cancellation
	s.lastProbe = time.Now()
}

// ResetBackoff clears backoff and attempt tracking without modifying the
// last-probe timestamp.  Useful after a recovery epoch change.
func (s *ProbeScheduler) ResetBackoff() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.backoff = s.Config.InitialBackoff
	s.attempts = 0
}

// Elapsed returns the time since the last probe, or zero if never probed.
func (s *ProbeScheduler) Elapsed(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastProbe.IsZero() {
		return 0
	}
	return now.Sub(s.lastProbe)
}

// Stale reports whether a probe result from the given epoch generation is
// stale.  It delegates to the authoritative RecoveryEpoch lifecycle owner
// rather than maintaining a separate generation counter.
func (s *ProbeScheduler) Stale(generation uint64) bool {
	if s.epoch == nil {
		return false
	}
	return !s.epoch.IsCurrent(generation)
}