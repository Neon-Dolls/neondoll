// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// --- test candidates ---

type validCandidate struct{}

func (c *validCandidate) Validate(ctx context.Context) (netip.AddrPort, error) {
	return netip.MustParseAddrPort("127.0.0.1:51820"), nil
}

type failingCandidate struct {
	err error
}

func (c *failingCandidate) Validate(ctx context.Context) (netip.AddrPort, error) {
	return netip.AddrPort{}, c.err
}

type blockingCandidate struct {
	ready chan struct{}
	block chan struct{}
	err   error
}

func (c *blockingCandidate) Validate(ctx context.Context) (netip.AddrPort, error) {
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
	select {
	case <-c.block:
		return netip.AddrPort{}, c.err
	case <-ctx.Done():
		return netip.AddrPort{}, ctx.Err()
	}
}

// --- helpers ---

func newMinimalTunnelForTest(t *testing.T) *BodyTunnel {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := BodyTunnelConfig{
		PrivateKey:     [32]byte{1, 2, 3, 4},
		CorePublicKey:  [32]byte{5, 6, 7, 8},
		OverlayAddress: netip.MustParseAddr("fd00::1"),
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   "127.0.0.1:51820",
	}
	return NewBodyTunnel(cfg, log)
}

func testMigrationConfig() MigrationConfig {
	return MigrationConfig{ProbeTimeout: 3 * time.Second}
}

// --- tests ---

func TestMigratePath_Basic(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	gen, err := MigratePath(context.Background(), epoch, &validCandidate{}, testMigrationConfig(), log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen == 0 {
		t.Fatal("expected non-zero generation")
	}
	if got := epoch.Active(); got != tunnel {
		t.Fatal("MigratePath replaced the active tunnel; it should be preserved")
	}
}

func TestMigratePath_FailedCandidate(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	candidate := &failingCandidate{err: errors.New("not viable")}
	gen, err := MigratePath(context.Background(), epoch, candidate, testMigrationConfig(), log)
	if err == nil {
		t.Fatal("expected error for failing candidate")
	}
	if gen != 0 {
		t.Fatalf("expected zero gen on failure, got %d", gen)
	}
	if got := epoch.Active(); got != tunnel {
		t.Fatal("failed migration replaced the active tunnel")
	}
}

func TestMigratePath_Cancellation(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	candidate := &blockingCandidate{
		ready: make(chan struct{}),
		block: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	var gen uint64
	var err error
	go func() {
		defer wg.Done()
		gen, err = MigratePath(ctx, epoch, candidate, testMigrationConfig(), log)
	}()

	<-candidate.ready
	cancel()
	wg.Wait()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if gen != 0 {
		t.Fatalf("expected zero gen on cancellation, got %d", gen)
	}
	if got := epoch.Active(); got != tunnel {
		t.Fatal("cancelled migration replaced the active tunnel")
	}
}

func TestMigratePath_Superseded(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	candidate := &blockingCandidate{
		ready: make(chan struct{}),
		block: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var gen uint64
	var err error
	go func() {
		defer wg.Done()
		gen, err = MigratePath(context.Background(), epoch, candidate, testMigrationConfig(), log)
	}()

	// Wait for Validate to be entered.
	<-candidate.ready

	// Bump the epoch generation while validation is blocked.
	epoch.NextGen()

	// Unblock validation — TryReserveMigration should fail because gen changed.
	close(candidate.block)
	wg.Wait()

	if !errors.Is(err, ErrMigrationSuperseded) {
		t.Fatalf("expected ErrMigrationSuperseded, got %v", err)
	}
	if gen != 0 {
		t.Fatalf("expected zero gen on supersession, got %d", gen)
	}
	if got := epoch.Active(); got != tunnel {
		t.Fatal("superseded migration replaced the active tunnel")
	}
}

func TestTryReserveMigration_Abandoned(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)

	// Reserve a generation via NextGenForTunnel.
	gen := epoch.NextGenForTunnel(tunnel)
	if gen == 0 {
		t.Fatal("migration failed to reserve epoch gen")
	}

	// Commit via TryReserveMigration — ownership stays reserved.
	if !epoch.TryReserveMigration(gen) {
		t.Fatal("TryReserveMigration should succeed for current gen")
	}

	// Ownership is reserved: NextGenForTunnel on the same tunnel
	// is blocked by the duplicate guard.
	if nextGen := epoch.NextGenForTunnel(tunnel); nextGen != 0 {
		t.Fatal("NextGenForTunnel should be blocked while migration owns reservation")
	}

	// Abandon: release ownership via TryCancel.
	epoch.TryCancel(gen)

	// After release, NextGenForTunnel on the same tunnel works again.
	if nextGen := epoch.NextGenForTunnel(tunnel); nextGen == 0 {
		t.Fatal("NextGenForTunnel should succeed after TryCancel released ownership")
	}
}

func TestTryReserveMigration_ClosedEpoch(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)

	// Reserve a generation before shutdown.
	gen := epoch.NextGenForTunnel(tunnel)
	if gen == 0 {
		t.Fatal("migration failed to reserve epoch gen")
	}

	// Shut down the epoch.
	epoch.Shutdown()

	// TryReserveMigration must reject after shutdown.
	if epoch.TryReserveMigration(gen) {
		t.Fatal("TryReserveMigration should fail after epoch shutdown")
	}

	// The tunnel was stopped by Shutdown — active is nil.
	if got := epoch.Active(); got != nil {
		t.Fatal("expected nil active after shutdown")
	}
}

func TestMigratePath_ConcurrentMigration(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First migration succeeds normally.
	gen1, err := MigratePath(context.Background(), epoch, &validCandidate{}, testMigrationConfig(), log)
	if err != nil {
		t.Fatalf("first migration should succeed: %v", err)
	}
	if gen1 == 0 {
		t.Fatal("expected non-zero gen from first migration")
	}

	// Ownership is reserved — pendingRecovery stays set so
	// NextGenForTunnel rejects a second attempt on the same tunnel.
	_, err2 := MigratePath(context.Background(), epoch, &validCandidate{}, testMigrationConfig(), log)
	if err2 == nil {
		t.Fatal("second migration on same tunnel should be rejected (duplicate guard)")
	}

	// First migration's gen is still valid — active tunnel preserved.
	if got := epoch.Active(); got != tunnel {
		t.Fatal("migration replaced the active tunnel")
	}
}

func TestMigratePath_Shutdown(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Shut down before migration attempt.
	epoch.Shutdown()

	// Active is nil after shutdown — MigratePath should fail fast.
	_, err := MigratePath(context.Background(), epoch, &validCandidate{}, testMigrationConfig(), log)
	if err == nil {
		t.Fatal("expected error after epoch shutdown")
	}
}

func TestMigratePath_Ownership_Epoch(t *testing.T) {
	tunnel := newMinimalTunnelForTest(t)
	epoch := NewRecoveryEpoch(tunnel)

	// Migration reserves the epoch gen.
	gen := epoch.NextGenForTunnel(tunnel)
	if gen == 0 {
		t.Fatal("migration failed to reserve epoch gen")
	}

	// A concurrent NextGen (from recovery) advances the epoch,
	// making the migration's gen stale.
	epoch.NextGen()

	// The migration's gen is now stale — TryReserveMigration fails.
	if epoch.TryReserveMigration(gen) {
		t.Fatal("gen should be stale after epoch was advanced")
	}

	// Active tunnel was never modified.
	if got := epoch.Active(); got != tunnel {
		t.Fatal("epoch active tunnel was replaced without WG cutover")
	}
}