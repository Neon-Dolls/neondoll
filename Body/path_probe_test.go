// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// encodeProbeRequest / verifyProbeResponse
// ---------------------------------------------------------------------------

func TestEncodeProbeRequest(t *testing.T) {
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	data := encodeProbeRequest(42, nonce, "test-cred")
	if len(data) != 52 {
		t.Fatalf("expected 52 bytes, got %d", len(data))
	}
	if string(data[:4]) != "NLPR" {
		t.Fatalf("bad magic: %q", string(data[:4]))
	}
	if got := binary.LittleEndian.Uint64(data[4:12]); got != 42 {
		t.Fatalf("bad routeID: got %d, want 42", got)
	}
	for i := 0; i < 8; i++ {
		if data[12+i] != byte(i) {
			t.Fatalf("bad nonce at %d: got %d, want %d", i, data[12+i], i)
		}
	}
}

func TestVerifyProbeResponse_Valid(t *testing.T) {
	nonce := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	var routeID uint64 = 99
	cred := "secret-token"

	// Build a properly-formed response exactly as the relay would.
	resp := make([]byte, 52)
	copy(resp[:4], "NLPS")
	binary.LittleEndian.PutUint64(resp[4:12], routeID)
	copy(resp[12:20], nonce)
	mac := computeProbeHMAC("NLPS", routeID, nonce, cred)
	copy(resp[20:52], mac)

	if err := verifyProbeResponse(resp, routeID, nonce, cred); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyProbeResponse_BadMagic(t *testing.T) {
	resp := make([]byte, 52)
	copy(resp[:4], "BADM")
	if err := verifyProbeResponse(resp, 0, make([]byte, 8), ""); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

func TestVerifyProbeResponse_BadHMAC(t *testing.T) {
	resp := make([]byte, 52)
	copy(resp[:4], "NLPS")
	// Write correct routeID and nonce but stale HMAC.
	binary.LittleEndian.PutUint64(resp[4:12], 1)
	copy(resp[12:20], []byte{0, 0, 0, 0, 0, 0, 0, 0})
	copy(resp[20:52], make([]byte, 32)) // all-zero HMAC

	if err := verifyProbeResponse(resp, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0}, "correct-cred"); err == nil {
		t.Fatal("expected error for bad HMAC")
	}
}

func TestVerifyProbeResponse_TooShort(t *testing.T) {
	if err := verifyProbeResponse([]byte("short"), 0, nil, ""); err == nil {
		t.Fatal("expected error for short buffer")
	}
}

// ---------------------------------------------------------------------------
// ReachProbe
// ---------------------------------------------------------------------------

// testProbeServer starts a UDP server that responds to probe requests
// exactly as the relay's ProbeListener does.
func testProbeServer(t *testing.T, credential string) (addr string, cleanup func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 52)
		for {
			select {
			case <-done:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if n != 52 || string(buf[:4]) != "NLPR" {
				continue
			}
			routeID := binary.LittleEndian.Uint64(buf[4:12])
			nonce := make([]byte, 8)
			copy(nonce, buf[12:20])

			mac := computeProbeHMAC("NLPS", routeID, nonce, credential)
			resp := make([]byte, 52)
			copy(resp[:4], "NLPS")
			binary.LittleEndian.PutUint64(resp[4:12], routeID)
			copy(resp[12:20], nonce)
			copy(resp[20:52], mac)
			conn.WriteToUDP(resp, src)
		}
	}()
	cleanup = func() {
		close(done)
		conn.Close()
	}
	return conn.LocalAddr().String(), cleanup
}

func TestReachProbe_Success(t *testing.T) {
	serverAddr, cleanup := testProbeServer(t, "test-cred")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := ReachProbe(ctx, ReachProbeConfig{
		Target:     serverAddr,
		RouteID:    1,
		Credential: "test-cred",
		Timeout:    1 * time.Second,
	})
	if !result.Reachable {
		t.Fatalf("expected reachable, got error: %v", result.Error)
	}
}

func TestReachProbe_WrongCredential(t *testing.T) {
	// Server expects "test-cred", caller uses "wrong-cred".
	serverAddr, cleanup := testProbeServer(t, "test-cred")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := ReachProbe(ctx, ReachProbeConfig{
		Target:     serverAddr,
		RouteID:    1,
		Credential: "wrong-cred",
		Timeout:    200 * time.Millisecond,
	})
	if result.Reachable {
		t.Fatal("expected unreachable for wrong credential")
	}
	if result.Error == nil {
		t.Fatal("expected non-nil error for wrong credential")
	}
}

func TestReachProbe_Timeout(t *testing.T) {
	// Point at a closed port — nobody responds.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := ReachProbe(ctx, ReachProbeConfig{
		Target:     "127.0.0.1:9999",
		RouteID:    1,
		Credential: "cred",
		Timeout:    50 * time.Millisecond,
	})
	if result.Reachable {
		t.Fatal("expected unreachable for closed port")
	}
	if result.Error == nil {
		t.Fatal("expected non-nil error for timeout")
	}
}

// ---------------------------------------------------------------------------
// ProbeScheduler
// ---------------------------------------------------------------------------

func TestNewProbeScheduler(t *testing.T) {
	cfg := DefaultProbeSchedulerConfig()
	s := NewProbeScheduler(cfg, 0)
	if s == nil {
		t.Fatal("NewProbeScheduler returned nil")
	}
	if s.RecoveryEpoch != 0 {
		t.Fatalf("expected epoch 0, got %d", s.RecoveryEpoch)
	}
}

func TestShouldProbe_InitiallyTrue(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Hour,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: time.Minute,
	}, 0)
	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true for a fresh scheduler")
	}
}

func TestShouldProbe_MinInterval(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    50 * time.Millisecond,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: time.Minute,
	}, 0)

	// First probe allowed.
	now := time.Now()
	if !s.ShouldProbe(now) {
		t.Fatal("ShouldProbe should be true initially")
	}

	s.RecordResult(false) // sets lastProbe = time.Now()

	// Immediately after — ShouldProbe should be false.
	if s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be false right after probing")
	}

	// After MinInterval passes — ShouldProbe should be true again.
	time.Sleep(60 * time.Millisecond)
	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after MinInterval")
	}
}

func TestBackoff_GrowsOnFailure(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Nanosecond,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: 10 * time.Millisecond,
	}, 0)

	s.RecordResult(false) // backoff → 10ms
	s.RecordResult(false) // backoff → 20ms
	s.RecordResult(false) // backoff → 40ms

	// Wait for backoff to expire plus MinInterval.
	time.Sleep(50 * time.Millisecond)

	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after waiting for backoff")
	}
}

func TestBackoff_CappedAtMaxBackoff(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Nanosecond,
		MaxBackoff:     15 * time.Millisecond,
		MaxAttempts:    100,
		InitialBackoff: 5 * time.Millisecond,
	}, 0)

	// Fail many times — backoff should cap at 15ms.
	for i := 0; i < 10; i++ {
		s.RecordResult(false)
	}

	// Each failure imposes InitialBackoff * 2^attempts = 5*2^5 = 160ms > 15ms cap.
	// Wait well past MaxBackoff.
	time.Sleep(20 * time.Millisecond)

	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after waiting for capped backoff")
	}
}

func TestMaxAttempts_Exhaustion(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Nanosecond,
		MaxBackoff:     10 * time.Millisecond,
		MaxAttempts:    3,
		InitialBackoff: time.Nanosecond,
	}, 0)

	s.RecordResult(false) // attempt 1
	s.RecordResult(false) // attempt 2
	s.RecordResult(false) // attempt 3 — exceeds MaxAttempts

	time.Sleep(1 * time.Millisecond) // ample time past any backoff
	if s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be false after MaxAttempts exhaustion")
	}
}

func TestRecordResult_ResetsBackoff(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Nanosecond,
		MaxBackoff:     time.Hour,
		MaxAttempts:    3,
		InitialBackoff: 50 * time.Millisecond,
	}, 0)

	s.RecordResult(false) // attempt 1 — backoff 50ms
	s.RecordResult(false) // attempt 2 — backoff 100ms
	s.RecordResult(true)  // success — resets everything

	time.Sleep(1 * time.Millisecond) // well past MinInterval

	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after success resets state")
	}
}

func TestRecordResult_SuccessResetsMaxAttempts(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Nanosecond,
		MaxBackoff:     time.Hour,
		MaxAttempts:    2,
		InitialBackoff: time.Nanosecond,
	}, 0)

	// Exhaust max attempts.
	s.RecordResult(false) // attempt 1
	s.RecordResult(false) // attempt 2 — MaxAttempts exhausted
	if s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be false after exhaustion")
	}

	// Success should reset the attempt counter.
	s.RecordResult(true)
	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after success resets attempts")
	}
}

func TestRecordCancelled_ResetsAndEnforcesMinInterval(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    50 * time.Millisecond,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: time.Minute,
	}, 0)

	// Should be probeable initially.
	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true initially")
	}

	s.RecordCancelled()

	// Immediately after cancellation — ShouldProbe should be false due to
	// MinInterval enforcement (RecordCancelled updates lastProbe).
	if s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be false right after RecordCancelled (MinInterval)")
	}

	// After MinInterval passes — should allow probing again.
	time.Sleep(60 * time.Millisecond)
	if !s.ShouldProbe(time.Now()) {
		t.Fatal("ShouldProbe should be true after MinInterval")
	}
}

func TestStale_DetectsEpochChange(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Hour,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: time.Minute,
	}, 0)

	// Stale returns true when the given epoch doesn't match the scheduler's
// current epoch.
	s.Stale(1) // 1 != 0 → true

	if s.Stale(0) {
		t.Fatal("Stale(0) should be false (current epoch 0 matches)")
	}
	if !s.Stale(1) {
		t.Fatal("Stale(1) should be true (epoch 1 != current epoch 0)")
	}
}

func TestResetEpoch(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    time.Hour,
		MaxBackoff:     time.Hour,
		MaxAttempts:    10,
		InitialBackoff: time.Minute,
	}, 0)

	s.ResetEpoch(5)
		if s.RecoveryEpoch != 5 {
			t.Fatalf("expected epoch 5, got %d", s.RecoveryEpoch)
		}
		if !s.Stale(4) {
			t.Fatal("Stale(4) should be true (epoch 4 != current epoch 5)")
		}
		if s.Stale(5) {
			t.Fatal("Stale(5) should be false (epoch 5 matches current epoch)")
		}
}

func TestElapsed(t *testing.T) {
	s := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval: time.Hour,
	}, 0)

	if s.Elapsed(time.Now()) != 0 {
		t.Fatal("Elapsed should be zero before any probe")
	}

	s.RecordResult(true)
	el := s.Elapsed(time.Now())
	if el <= 0 {
		t.Fatal("Elapsed should be positive after probing")
	}
}