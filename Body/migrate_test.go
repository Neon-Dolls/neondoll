package body

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test candidates
// ---------------------------------------------------------------------------

type validCandidate struct {
	endpoint string
}

func (c *validCandidate) Validate(ctx context.Context) (*MigrationTarget, error) {
	return &MigrationTarget{CoreEndpoint: c.endpoint}, nil
}

func (c *validCandidate) Stop() {}

type failingCandidate struct{}

func (c *failingCandidate) Validate(ctx context.Context) (*MigrationTarget, error) {
	return nil, errors.New("nope")
}

func (c *failingCandidate) Stop() {}

type blockingCandidate struct {
	block chan struct{}
}

func (c *blockingCandidate) Validate(ctx context.Context) (*MigrationTarget, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.block:
		return &MigrationTarget{CoreEndpoint: "127.0.0.1:9999"}, nil
	}
}

func (c *blockingCandidate) Stop() {
	close(c.block)
}

// ---------------------------------------------------------------------------
// MigratePath unit tests
// ---------------------------------------------------------------------------

func TestMigratePath_ValidationFailure(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen, err := MigratePath(
		context.Background(),
		epoch,
		&failingCandidate{},
		MigrationConfig{ProbeTimeout: time.Second},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err == nil {
		t.Fatal("expected error from failing candidate")
	}
	if gen != 0 {
		t.Fatalf("expected gen=0 on failure, got %d", gen)
	}
}

func TestMigratePath_ValidationTimeout(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	block := make(chan struct{})
	gen, err := MigratePath(
		context.Background(),
		epoch,
		&blockingCandidate{block: block},
		MigrationConfig{ProbeTimeout: 10 * time.Millisecond},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if gen != 0 {
		t.Fatalf("expected gen=0 on failure, got %d", gen)
	}
}

func TestMigratePath_SuccessReturnsGen(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen, err := MigratePath(
		context.Background(),
		epoch,
		&validCandidate{endpoint: "10.0.0.1:51820"},
		MigrationConfig{ProbeTimeout: time.Second},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen == 0 {
		t.Fatal("expected non-zero gen on success")
	}
}

func TestMigratePath_DefaultTimeout(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen, err := MigratePath(
		context.Background(),
		epoch,
		&validCandidate{endpoint: "10.0.0.1:51820"},
		MigrationConfig{}, // zero timeout
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err != nil {
		t.Fatalf("unexpected error with zero config: %v", err)
	}
	if gen == 0 {
		t.Fatal("expected non-zero gen")
	}
}

// ---------------------------------------------------------------------------
// MigrationCandidate interface compliance
// ---------------------------------------------------------------------------

func TestMigrationCandidate_ValidateReturnsTarget(t *testing.T) {
	c := &validCandidate{endpoint: "10.0.0.1:51820"}
	target, err := c.Validate(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if target == nil {
		t.Fatal("expected non-nil MigrationTarget")
	}
	if target.CoreEndpoint != "10.0.0.1:51820" {
		t.Fatalf("expected 10.0.0.1:51820, got %q", target.CoreEndpoint)
	}
	if target.Bind != nil {
		t.Fatal("expected nil Bind for default candidate")
	}
}

func TestMigrationCandidate_FailReturnsNilTarget(t *testing.T) {
	c := &failingCandidate{}
	target, err := c.Validate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if target != nil {
		t.Fatal("expected nil target on failure")
	}
}

// ---------------------------------------------------------------------------
// CutoverPath gen guard (no tunnel involved)
// ---------------------------------------------------------------------------
// These tests verify CutoverPath rejects stale generations before it
// touches the old tunnel.  oldTunnel=nil is safe because the check runs
// before any tunnel operation.

func TestCutoverPath_StaleGenRejectedBeforeBreak(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen := epoch.NextGen() // gen = 1
	epoch.NextGen()        // advance to 2 — gen is stale

	result := CutoverPath(
		context.Background(),
		nil, // oldTunnel — unused because stale gen is caught before BREAK
		epoch,
		&validCandidate{endpoint: "10.0.0.1:51820"},
		gen, // stale
		MigrationConfig{ProbeTimeout: time.Second},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if result.Err == nil {
		t.Fatal("expected error for stale generation")
	}
	if !errors.Is(result.Err, ErrMigrationSuperseded) {
		t.Fatalf("expected ErrMigrationSuperseded, got: %v", result.Err)
	}
	if result.Tunnel != nil {
		t.Fatal("expected nil tunnel for stale gen")
	}
}

// ---------------------------------------------------------------------------
// TryReserveMigration / IsCurrent consistency
// ---------------------------------------------------------------------------

func TestTryReserveMigration_CurrentGen(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen := epoch.NextGen()
	if !epoch.TryReserveMigration(gen) {
		t.Fatal("expected TryReserveMigration to accept current gen")
	}
}

func TestTryReserveMigration_StaleGen(t *testing.T) {
	epoch := NewRecoveryEpoch(nil)
	gen := epoch.NextGen()
	epoch.NextGen() // advance
	if epoch.TryReserveMigration(gen) {
		t.Fatal("expected TryReserveMigration to reject stale gen")
	}
}
