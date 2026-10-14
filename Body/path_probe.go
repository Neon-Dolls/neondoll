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
	defer conn.Close()

	// Deadline for the entire exchange — honour whichever is shorter:
	// the caller's context deadline or the per-probe Timeout.
	deadline := start.Add(cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return ReachProbeResult{Error: fmt.Errorf("reach probe: nonce: %w", err), Duration: time.Since(start)}
	}

	req := encodeProbeRequest(cfg.RouteID, nonce, cfg.Credential)
	if _, err := conn.Write(req); err != nil {
		return ReachProbeResult{Error: fmt.Errorf("reach probe: write: %w", err), Duration: time.Since(start)}
	}

	resp := make([]byte, 52)
	n, err := conn.Read(resp)
	if err != nil {
		return ReachProbeResult{Error: fmt.Errorf("reach probe: read: %w", err), Duration: time.Since(start)}
	}

	if err := verifyProbeResponse(resp[:n], cfg.RouteID, nonce, cfg.Credential); err != nil {
		return ReachProbeResult{Error: fmt.Errorf("reach probe: verify: %w", err), Duration: time.Since(start)}
	}

	return ReachProbeResult{Reachable: true, Duration: time.Since(start)}
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
// exponential backoff on failure.  It is safe for concurrent use.
type ProbeScheduler struct {
	Config     ProbeSchedulerConfig
	RecoveryEpoch int // epoch at construction time for stale-result rejection

	mu          sync.Mutex
	lastProbe   time.Time
	backoff     time.Duration
	attempts    int
}

// ProbeSchedulerConfig configures probe scheduling behaviour.
type ProbeSchedulerConfig struct {
	// MinInterval is the minimum time between consecutive probe cycles.
	// The scheduler will not allow more than one probe every MinInterval.
	MinInterval time.Duration

	// MaxBackoff caps the exponential backoff duration.
	MaxBackoff time.Duration

	// MaxAttempts is the maximum consecutive failed attempts before the
	// scheduler gives up (ShouldProbe returns false).  Zero means no limit.
	MaxAttempts int

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
func NewProbeScheduler(cfg ProbeSchedulerConfig, epoch int) *ProbeScheduler {
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
		Config:        cfg,
		RecoveryEpoch: epoch,
		backoff:       cfg.InitialBackoff,
	}
}

// ShouldProbe reports whether a new probe cycle is permitted.  If false,
// the caller should wait and check again before probing.
func (s *ProbeScheduler) ShouldProbe(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce minimum interval between probe cycles.
	if !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < s.Config.MinInterval {
		return false
	}

	// Enforce max attempts.
	if s.Config.MaxAttempts > 0 && s.attempts >= s.Config.MaxAttempts {
		return false
	}

	return true
}

// BackoffDuration returns how long the caller should wait before the next
// probe attempt.  It accounts for both the MinInterval and exponential
// backoff.
func (s *ProbeScheduler) BackoffDuration(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	interval := s.Config.MinInterval
	if !s.lastProbe.IsZero() {
		elapsed := now.Sub(s.lastProbe)
		if elapsed < interval {
			interval -= elapsed
		} else {
			interval = 0
		}
	}

	return time.Duration(math.Max(float64(interval), float64(s.backoff)))
}

// RecordResult feeds back a probe result into the scheduler's backoff and
// attempt tracking.  reachable=true resets the backoff; reachable=false
// increases it exponentially up to MaxBackoff.
func (s *ProbeScheduler) RecordResult(reachable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastProbe = time.Now()

	if reachable {
		// Successful probe resets backoff and attempts.
		s.backoff = s.Config.InitialBackoff
		s.attempts = 0
	} else {
		s.attempts++
		s.backoff = time.Duration(math.Min(
			float64(s.Config.InitialBackoff)*math.Pow(2, float64(s.attempts-1)),
			float64(s.Config.MaxBackoff),
		))
	}
}

// RecordCancelled resets the scheduler so the next ShouldProbe call will
// allow probing (after the MinInterval).  The lastProbe timestamp is
// updated to now so that MinInterval is enforced from the cancellation
// point.
func (s *ProbeScheduler) RecordCancelled() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.backoff = s.Config.InitialBackoff
	s.attempts = 0
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

// Stale reports whether a result belongs to an old epoch.  Call with the
// current epoch to detect stale results after a recovery cycle.
func (s *ProbeScheduler) Stale(currentEpoch int) bool {
	return currentEpoch != s.RecoveryEpoch
}

// ResetEpoch updates the scheduler's epoch.  The caller should call this
// when the path selector starts a new recovery cycle.
func (s *ProbeScheduler) ResetEpoch(epoch int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.RecoveryEpoch = epoch
}