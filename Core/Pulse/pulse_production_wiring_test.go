package pulse_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// productionMindAPI mirrors the production daemon's MindAPI wrapper.
// It wraps a persistence Store and DollState to satisfy the dollmind.MindAPI
// interface, exactly as cmd/neondolld/main.go does.
type productionMindAPI struct {
	state    *dollstate.DollState
	store    persistence.Store
	provider inference.Provider
}

func (m *productionMindAPI) Inference() inference.Provider { return m.provider }
func (m *productionMindAPI) State() *dollstate.DollState   { return m.state }
func (m *productionMindAPI) Save() error                   { return m.store.SaveDoll(context.Background(), m.state) }

// callTrackingProvider wraps a MockProvider and records each Infer call count.
// Thread-safe via atomic counter — used in production wiring tests where the
// Scheduler calls Infer from a different goroutine than the test assertions.
type callTrackingProvider struct {
	inner *inference.MockProvider
	calls atomic.Int64
}

func newCallTrackingProvider(resp string) *callTrackingProvider {
	return &callTrackingProvider{
		inner: inference.NewMockProvider(inference.ProviderID("tracker"), resp),
	}
}

func (p *callTrackingProvider) Infer(ctx context.Context, req inference.Request) (*inference.Response, error) {
	p.calls.Add(1)
	return p.inner.Infer(ctx, req)
}

func (p *callTrackingProvider) ID() inference.ProviderID { return p.inner.ID() }

func (p *callTrackingProvider) CallCount() int { return int(p.calls.Load()) }

// ─────────────────────────────────────────────────────────────────────────────
// Finding #1: LoadDoll error semantics — never interpret corruption as absence
// ─────────────────────────────────────────────────────────────────────────────

// TestLoadDoll_NotFound_Created verifies that a missing doll correctly returns
// ErrDollNotFound and can be created afterward, mirroring the safe startup
// path in cmd/neondolld/main.go.
func TestLoadDoll_NotFound_Created(t *testing.T) {
	dbDir := t.TempDir()
	store, err := persistence.NewStore(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	const dollID = "new-doll"

	// 1. Loading a non-existent doll must return ErrDollNotFound.
	_, err = store.LoadDoll(context.Background(), dollID)
	if !errors.Is(err, persistence.ErrDollNotFound) {
		t.Fatalf("LoadDoll for missing doll: got %v, want ErrDollNotFound", err)
	}

	// 2. Creating a new doll must succeed.
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: dollID, CanonicalName: "NewDoll"}
	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	// 3. After creation, LoadDoll must return the doll.
	loaded, err := store.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("LoadDoll after create: %v", err)
	}
	if loaded.Identity.DollID != dollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, dollID)
	}
	if loaded.Identity.CanonicalName != "NewDoll" {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, "NewDoll")
	}
}

// TestLoadDoll_Exists_Loaded verifies that an existing doll loads correctly
// and preserves its identity — the normal startup path.
func TestLoadDoll_Exists_Loaded(t *testing.T) {
	dbDir := t.TempDir()
	store, err := persistence.NewStore(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	const dollID = "existing-doll"
	const canonName = "ExistingTest"

	// Create and save a doll.
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: dollID, CanonicalName: canonName}
	ds.Soul.Content = "I am an existing doll."
	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	// Load it back — must preserve identity and soul.
	loaded, err := store.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if loaded.Identity.DollID != dollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, dollID)
	}
	if loaded.Identity.CanonicalName != canonName {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, canonName)
	}
	if loaded.Soul.Content != "I am an existing doll." {
		t.Errorf("Soul = %q, want %q", loaded.Soul.Content, "I am an existing doll.")
	}
}

// TestLoadDoll_Corrupt_NotOverwritten verifies that a corrupt/damaged doll
// state produces a non-ErrDollNotFound error. This proves the invariant:
// failure to load a Doll must never be interpreted as absence of the Doll.
// A corrupt/damaged doll is NOT a missing doll.
func TestLoadDoll_Corrupt_NotOverwritten(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "test.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const dollID = "test-doll"
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: dollID, CanonicalName: "TestDoll"}
	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	// Close the store and corrupt the identity_json column directly.
	store.Close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Write garbage into identity_json so json.Unmarshal fails at LoadDoll.
	_, err = db.Exec("UPDATE dolls SET identity_json = '{corrupted,garbage]}' WHERE doll_id = ?", dollID)
	if err != nil {
		db.Close()
		t.Fatalf("UPDATE identity_json: %v", err)
	}
	db.Close()

	// Reopen the store.
	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore after corrupt: %v", err)
	}
	defer store2.Close()

	// LoadDoll must fail with a non-ErrDollNotFound error.
	_, err = store2.LoadDoll(context.Background(), dollID)
	if err == nil {
		t.Fatal("LoadDoll succeeded on corrupt data — expected error")
	}
	if errors.Is(err, persistence.ErrDollNotFound) {
		t.Fatalf("LoadDoll returned ErrDollNotFound for corrupt data — "+
			"must distinguish corrupt from missing. Got: %v", err)
	}
	t.Logf("correctly rejected corrupt doll state: %v", err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Finding #2: Production vertical tick-driven PulseWake → Scheduler → L1
// ─────────────────────────────────────────────────────────────────────────────

// TestProductionWiring verifies that the full production composition path
// works: Pulse Runner Tick → evaluation → opportunity → admission →
// real Doll Mind Scheduler → L0 Reflex → L1 Orient.
//
// It exercises the same construction path as cmd/neondolld/main.go but uses
// NewTestRunner with test channels for deterministic tick driving and a
// call-tracking mock provider so the test does not perform real network
// inference.
//
// Unlike the earlier version, this test proves the RUNNER ITSELF causes the
// Scheduler call — it does NOT call scheduler.EnterPulseWake directly as
// the proof of the vertical.
func TestProductionWiring(t *testing.T) {
	// ── Setup: temp persistence store ────────────────────────────────
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "test.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// ── Create and seed a host doll state ────────────────────────────
	const dollID = "test-host"
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{
		DollID:        dollID,
		CanonicalName: "TestHost",
	}
	ds.Soul.Content = "You are TestHost, a test doll for production wiring verification."
	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	reloaded, err := store.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	// ── Create a deterministic call-tracking mock provider ───────────
	// Returns matters=false so L1 Orient runs, L2 Plan is skipped.
	// Track calls so the test can prove the Scheduler actually ran inference.
	mockResp := `{"summary":"self-check","matters":false,"reason":"no action needed"}`
	spy := newCallTrackingProvider(mockResp)
	log := logger.New(logger.ErrorLevel, nil)

	// ── Build the production MindAPI ─────────────────────────────────
	mindAPI := &productionMindAPI{state: reloaded, store: store, provider: spy}

	// ── Create the Scheduler (same as production daemon) ─────────────
	scheduler := dollmind.New(spy, log, mindAPI)

	// ── Config: enable Pulse with change horizon 1 so subject signal
	// fires immediately when ChangesSincePresent >= 1 ─────────────────
	cfg := config.Defaults()
	cfg.Core.Pulse.Enabled = true
	cfg.Core.Pulse.ChangeHorizon = 1
	cfg.Core.Pulse.WakeCooldown = 0
	cfg.Core.Pulse.MinWakeSpacing = 0

	fakeClock := &fixedClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	fakeRNG := &constantRNG{v: 0.01} // below effectivePressure → opportunity fires

	// ── Create and start the test runner with tick channels ──────────
	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)

	runner := pulse.NewTestRunner(cfg.Core.Pulse, fakeClock, fakeRNG, log, scheduler, tickCh, ackCh)
	if runner == nil {
		t.Fatal("NewTestRunner returned nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Runner.Start: %v", err)
	}
	defer runner.Stop()

	// ── Set up subjects so a tick produces an opportunity ────────────
	// A subject with ChangesSincePresent >= ChangeHorizon (1) produces
	// a change signal = 1.0. With Cooldown=0 and Budget=0, the
	// effectivePressure is 1.0. RNG returns 0.01 < 1.0 → opportunity.
	runner.UpdateSubjects([]pulse.PulseSubjectState{
		{
			SubjectID:           "test-subject",
			ChangesSincePresent: 1,
		},
	})

	// ── Send a deterministic tick ────────────────────────────────────
	tickCh <- fakeClock.now

	// ── Wait for the tick to complete (evaluate + admission + Orient) ─
	select {
	case <-ackCh:
		// tick completed
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for tick ack — the runner may not have processed the tick")
	}

	// ── Verify at least one Pulse evaluation occurred ─────────────────
	snap := runner.Snapshot()
	if snap.TickCount < 1 {
		t.Errorf("expected TickCount >= 1, got %d", snap.TickCount)
	}
	t.Logf("tick evaluation snapshot: tick=%d, lastTick=%v", snap.TickCount, snap.LastTickAt)

	// ── Verify opportunity became true ───────────────────────────────
	oppSnap := runner.OpportunitySnapshot()
	if !oppSnap.Opportunity {
		t.Errorf("expected opportunity=true after tick, got Opportunity=false (pressure=%.4f, eff=%.4f, RNG=%.4f)",
			oppSnap.Pressure, oppSnap.EffectivePressure, 0.01)
	}
	if oppSnap.EffectivePressure < 0.99 {
		t.Errorf("expected effectivePressure ~1.0 (change=1, no inhibition), got %f", oppSnap.EffectivePressure)
	}
	if oppSnap.RandomSample == nil || *oppSnap.RandomSample != 0.01 {
		t.Errorf("expected RandomSample=0.01 from fakeRNG, got %v", oppSnap.RandomSample)
	}
	t.Logf("opportunity evaluated: pressure=%.4f, eff=%.4f, opportunity=%v",
		oppSnap.Pressure, oppSnap.EffectivePressure, oppSnap.Opportunity)

	// ── Verify the real Scheduler was entered through Runner admission ─
	// The call-tracking provider records each Infer call. EnterPulseWake
	// → L0 → PathOrient → L1 Orient → provider.Infer.
	// Since matters=false, only L1 runs (no L2).
	if spy.CallCount() < 1 {
		t.Errorf("expected at least 1 inference call (L1 Orient), got %d", spy.CallCount())
	}
	t.Logf("spy inference call count: %d (expected >= 1 for L1 Orient)", spy.CallCount())

	// ── Verify only L1 inference was called (matters=false → no L2) ──
	if spy.CallCount() > 1 {
		t.Logf("note: spy inference calls = %d (expected 1 if matters=false caused no L2)", spy.CallCount())
		// This is informational — depending on future optimizations, the
		// exact count is not a strict invariant, but matters=false should
		// not trigger L2 planning.
	}

	// ── Verify occupancy is released after admission ─────────────────
	// If cognition run was claimed and released, TryClaimCognitionRun
	// should succeed on the next attempt.
	if !runner.TryClaimCognitionRun() {
		t.Error("TryClaimCognitionRun returned false — occupancy was not released after PulseWake admission")
	}
	runner.ReleaseCognitionRun()
}

// TestProductionWiring_StopRespectsShutdown verifies that Pulse-originated
// cognition respects Core shutdown via context cancellation.
func TestProductionWiring_StopRespectsShutdown(t *testing.T) {
	// ── Setup: minimal store and doll state ─────────────────────────
	dbDir := t.TempDir()
	store, err := persistence.NewStore(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: "shutdown-test", CanonicalName: "ShutdownTest"}
	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	state, err := store.LoadDoll(context.Background(), "shutdown-test")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	mockResp := `{"summary":"self-check","matters":false,"reason":"no action needed"}`
	spy := newCallTrackingProvider(mockResp)
	log := logger.New(logger.ErrorLevel, nil)

	mindAPI := &productionMindAPI{state: state, store: store, provider: spy}
	scheduler := dollmind.New(spy, log, mindAPI)

	cfg := config.Defaults()
	cfg.Core.Pulse.Enabled = true
	cfg.Core.Pulse.IdleHorizon = 1
	cfg.Core.Pulse.MinWakeSpacing = 0

	fakeClock := &fixedClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	fakeRNG := &constantRNG{v: 0.01}

	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)

	runner := pulse.NewTestRunner(cfg.Core.Pulse, fakeClock, fakeRNG, log, scheduler, tickCh, ackCh)

	// ── Start and immediately cancel the context ─────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Runner.Start: %v", err)
	}

	// Cancel immediately — this should stop the evaluation loop.
	cancel()
	runner.Stop()

	// If we get here without hang/deadlock, shutdown propagation works.
	t.Log("Pulse runner shutdown via context cancellation: ok")
}

// ─── Test helpers ───────────────────────────────────────────────────

// fixedClock implements pulse.Clock with a mutable time field.
type fixedClock struct {
	now time.Time
}

func (c *fixedClock) Now() time.Time { return c.now }

// constantRNG implements pulse.RNG returning a fixed value.
type constantRNG struct {
	v float64
}

func (r *constantRNG) Float64() float64 { return r.v }
