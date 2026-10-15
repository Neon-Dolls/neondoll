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

func (c *validCandidate) Validate(ctx context.Context) error {
	return nil
}

type failingCandidate struct {
	err error
}

func (c *failingCandidate) Validate(ctx context.Context) error {
	return c.err
}

type blockingCandidate struct {
	ready chan struct{}
	block chan struct{}
	err   error
}

func (c *blockingCandidate) Validate(ctx context.Context) error {
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
	select {
	case <-c.block:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
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