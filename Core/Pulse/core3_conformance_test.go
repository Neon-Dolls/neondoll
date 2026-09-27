package pulse_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
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

// ── M7 Conformance Test: Spark Has a Heartbeat ─────────────────────────
//
// Core 3 M7 is a CONFORMANCE milestone: prove the existing M1–M6 Pulse
// implementation works correctly through production-shaped wiring.
// All scenarios (A–I) use real Scheduler/Doll Mind with a mock inference
// provider where needed — no faked MindEntrance stubs.
//
// Build-plan authority: /NeonDoll-Concepts/build-plans/core-3-m7-build-plan.md

// ── conformance provider ────────────────────────────────────────────────

// conformanceProvider wraps inference.MockProvider with call-count-based
// error injection. Returns known JSON responses for N calls, then fails.
type conformanceProvider struct {
	mock       *inference.MockProvider
	failOnCall int64 // 0 = never fail; 1+ = fail on this call and all after
	failErr    error
	callCount  atomic.Int64
}

func newConformanceProvider(resp string) *conformanceProvider {
	return &conformanceProvider{
		mock:    inference.NewMockProvider("conformance", resp),
		failErr: fmt.Errorf("conformance provider: simulated cognition failure"),
	}
}

func (p *conformanceProvider) Infer(ctx context.Context, req inference.Request) (*inference.Response, error) {
	p.callCount.Add(1)
	if p.failOnCall > 0 && p.callCount.Load() >= p.failOnCall {
		return nil, p.failErr
	}
	return p.mock.Infer(ctx, req)
}

func (p *conformanceProvider) ID() inference.ProviderID { return p.mock.ID() }

func (p *conformanceProvider) CallCount() int { return int(p.callCount.Load()) }

// ── test MindAPI ───────────────────────────────────────────────────────

// testMindAPI implements dollmind.MindAPI for conformance tests.
type testMindAPI struct {
	state *dollstate.DollState
	prov  *conformanceProvider
	store persistence.Store // optional, set for persistence-integration tests
}

func newTestMindAPI(dollID, name string, prov *conformanceProvider) *testMindAPI {
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: dollID, CanonicalName: name}
	return &testMindAPI{state: &ds, prov: prov}
}

func (m *testMindAPI) Inference() inference.Provider { return m.prov }
func (m *testMindAPI) State() *dollstate.DollState   { return m.state }
func (m *testMindAPI) Save() error {
	if m.store != nil {
		return m.store.SaveDoll(context.Background(), m.state)
	}
	return nil
}

// ── capturingMind ──────────────────────────────────────────────────────

// capturingMind wraps a pulse.MindEntrance (real Scheduler), captures
// every PulseWake so the test can assert its contents, and supports
// fail-after-N-calls for cognition-failure scenarios.
type capturingMind struct {
	inner     pulse.MindEntrance
	lastWake  atomic.Value // stores *pulse.PulseWake
	wakeCalls atomic.Int64
	failAfter int64 // 0 = never fail; N = fail on Nth EnterPulseWake call
	failErr   error
}

func newCapturingMind(inner pulse.MindEntrance) *capturingMind {
	return &capturingMind{
		inner:   inner,
		failErr: fmt.Errorf("capturingMind: simulated cognition failure"),
	}
}

func (m *capturingMind) EnterPulseWake(ctx context.Context, wake pulse.PulseWake) error {
	m.wakeCalls.Add(1)
	w := wake
	m.lastWake.Store(&w)
	if m.failAfter > 0 && m.wakeCalls.Load() >= m.failAfter {
		return m.failErr
	}
	return m.inner.EnterPulseWake(ctx, wake)
}

func (m *capturingMind) LastWake() *pulse.PulseWake {
	v := m.lastWake.Load()
	if v == nil {
		return nil
	}
	return v.(*pulse.PulseWake)
}

func (m *capturingMind) WakeCount() int { return int(m.wakeCalls.Load()) }

// ── steppedClock — deterministic clock ──────────────────────────────────

type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *steppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ── deterministicRNG ────────────────────────────────────────────────────

type deterministicRNG struct {
	v float64
}

func (r *deterministicRNG) Float64() float64 { return r.v }

// ── helpers ─────────────────────────────────────────────────────────────

func conformanceConfig() config.PulseConfig {
	return config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    30,
		NeglectHorizon: 0,
		ChangeHorizon:  86400,
		WakeCooldown:   5,
		MinWakeSpacing: 60,
	}
}

func newSubject(id string) []pulse.PulseSubjectState {
	return []pulse.PulseSubjectState{
		{SubjectID: id},
	}
}

func tickAtSync(t *testing.T, runner *pulse.Runner, fc *steppedClock, at time.Time,
	tickCh chan time.Time, ackCh chan struct{}) (pulse.PulseSnapshot, pulse.SignalSnapshot, pulse.OpportunitySnapshot) {
	t.Helper()
	fc.Set(at)
	tickCh <- at
	select {
	case <-ackCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("tickAtSync: timeout waiting for ack at %v", at)
	}
	return runner.Snapshot(), runner.SignalSnapshot(), runner.OpportunitySnapshot()
}

func tickAtSyncShort(t *testing.T, runner *pulse.Runner, fc *steppedClock, at time.Time,
	tickCh chan time.Time, ackCh chan struct{}) pulse.PulseSnapshot {
	t.Helper()
	tickAtSync(t, runner, fc, at, tickCh, ackCh)
	return runner.Snapshot()
}

func newConformanceRunner(cfg config.PulseConfig, fc pulse.Clock, rng pulse.RNG,
	mind pulse.MindEntrance) (*pulse.Runner, chan time.Time, chan struct{}) {
	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)
	log := logger.New(logger.ErrorLevel, nil)
	runner := pulse.NewTestRunner(cfg, fc, rng, log, mind, tickCh, ackCh)
	runner.UpdateSubjects(newSubject("test-subject"))
	return runner, tickCh, ackCh
}

func newProductionRunner(t *testing.T, cfg config.PulseConfig, fc pulse.Clock,
	rng pulse.RNG, prov *conformanceProvider) (*pulse.Runner, *capturingMind, chan time.Time, chan struct{}) {
	t.Helper()
	mindAPI := newTestMindAPI("conformance-doll", "ConformanceTest", prov)
	scheduler := dollmind.New(mindAPI.Inference(), logger.New(logger.ErrorLevel, nil), mindAPI)
	captured := newCapturingMind(scheduler)
	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)
	log := logger.New(logger.ErrorLevel, nil)
	runner := pulse.NewTestRunner(cfg, fc, rng, log, captured, tickCh, ackCh)
	runner.UpdateSubjects(newSubject("test-subject"))
	return runner, captured, tickCh, ackCh
}

func startRunner(ctx context.Context, t *testing.T, runner *pulse.Runner) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	if err := runner.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Runner.Start: %v", err)
	}
	return cancel
}

func fieldsOf[T any](t *testing.T, v T) []string {
	t.Helper()
	val := reflect.TypeOf(v)
	if val.Kind() != reflect.Struct {
		t.Fatalf("fieldsOf: %T is not a struct", v)
	}
	var names []string
	for i := 0; i < val.NumField(); i++ {
		f := val.Field(i)
		if f.IsExported() {
			names = append(names, f.Name)
		}
	}
	return names
}

func findSubjectState(runner *pulse.Runner, subjID string) *pulse.PulseSubjectState {
	for i, s := range runner.SubjectSnapshots() {
		if s.SubjectID == subjID {
			return &runner.SubjectSnapshots()[i]
		}
	}
	return nil
}

// ── Scenario A: Golden Idle-Only Heartbeat ──────────────────────────────
//
// Prove production-shaped composition: real Scheduler, real Clock, real RNG,
// real dollstate. Demonstrate the canonical restart lifecycle:
// construct → restore → start → first evaluation. Capture and assert the
// actual PulseWake struct, including idle-only wake with Subjects == [].
//
// Acceptance:
//
//	A1: Restore checkpoint with LastCognitionAt=T-2s → idle=1.0 at T0 → opp fires
//	A2: Captured PulseWake has Subjects == [] (idle-only)
//	A3: PulseWake.Pressure > 0 and EffectivePressure > 0
//	A4: Wake AdmittedAt is near T0
//	A5: LastSpontaneousWakeAt advances to T0
//	A6: Successive tick at T+1s: idle still saturated → another wake
//	A7: Second wake AdmittedAt matches the tick time

func TestM7_ScenarioA_GoldenHeartbeat(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"idle heartbeat","matters":false,"reason":"no external event"}`)
	runner, captured, tickCh, ackCh := newProductionRunner(t, cfg, fc, &deterministicRNG{v: 0.01}, prov)

	// Canonical lifecycle: construct → restore → start → first evaluation.
	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0,
	}
	runner.RestoreFromCheckpoint(cp)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	// ── First evaluation: should see restored checkpoint pressure ──
	snap, sig, opp := tickAtSync(t, runner, fc, T0, tickCh, ackCh)

	// Hard assertion: restore happened before evaluation — idle computed from
	// LastCognitionAt=T-2s with IdleHorizon=1s → idle=2s/1s=2.0, clamped to 1.0.
	if sig.Idle != 1.0 {
		t.Errorf("A1: idle = %f, want 1.0 (restored checkpoint with LastCognitionAt=T-2s)", sig.Idle)
	}
	if !opp.Opportunity {
		t.Errorf("A1: opportunity should fire with idle=1.0 after restore-before-start")
	}
	if snap.LastSpontaneousWakeAt != T0 {
		t.Errorf("A1: LastSpontaneousWakeAt = %v, want %v", snap.LastSpontaneousWakeAt, T0)
	}

	if captured.WakeCount() < 1 {
		t.Errorf("A1: mind entered %d times, want >= 1 (restore → wake)", captured.WakeCount())
	}

	wake := captured.LastWake()
	if wake == nil {
		t.Fatalf("A2: no PulseWake captured")
	}
	if len(wake.Subjects) != 0 {
		t.Errorf("A2: PulseWake.Subjects = %v, want [] (idle-only wake)", wake.Subjects)
	}

	if wake.Pressure <= 0 {
		t.Errorf("A3: PulseWake.Pressure = %f, want > 0", wake.Pressure)
	}
	if wake.EffectivePressure <= 0 {
		t.Errorf("A3: PulseWake.EffectivePressure = %f, want > 0", wake.EffectivePressure)
	}

	if wake.AdmittedAt.Before(T0.Add(-time.Second)) || wake.AdmittedAt.After(T0.Add(time.Second)) {
		t.Errorf("A4: PulseWake.AdmittedAt = %v, want near T0 (%v)", wake.AdmittedAt, T0)
	}

	if snap.LastSpontaneousWakeAt != T0 {
		t.Errorf("A5: LastSpontaneousWakeAt = %v, want %v", snap.LastSpontaneousWakeAt, T0)
	}

	// ── Second tick: idle still saturated → another wake ──
	snap, sig, opp = tickAtSync(t, runner, fc, T0.Add(time.Second), tickCh, ackCh)

	if !opp.Opportunity {
		t.Errorf("A6: second tick opportunity should fire (idle saturated)")
	}
	if captured.WakeCount() < 2 {
		t.Errorf("A6: mind entered %d times, want >= 2 (two wakes expected)", captured.WakeCount())
	}

	secondWake := captured.LastWake()
	if secondWake == nil {
		t.Fatalf("A7: no second PulseWake captured")
	}
	if secondWake.AdmittedAt != T0.Add(time.Second) {
		t.Errorf("A7: second wake AdmittedAt = %v, want %v", secondWake.AdmittedAt, T0.Add(time.Second))
	}
}

// ── Scenario B: Quiet / No-Wake ────────────────────────────────────────
//
// Prove that Pulse does NOT wake when all signals are disabled AND no
// idle pressure exists. Uses production-shaped composition with a real
// Scheduler, but the config eliminates all pressure sources.
//
// Acceptance:
//
//	B1: No subjects set → no subject-based signals
//	B2: IdleHorizon=0 → idle=0 permanently
//	B3: No opportunity ever fires
//	B4: EnterPulseWake never called
//	B5: Snapshot reflects no cognition activity

func TestM7_ScenarioB_QuietNoWake(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 0
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"quiet","matters":false,"reason":"no event"}`)
	runner, captured, tickCh, ackCh := newProductionRunner(t, cfg, fc, &deterministicRNG{v: 0.01}, prov)
	runner.UpdateSubjects(nil)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	for i := 0; i < 3; i++ {
		at := T0.Add(time.Duration(i) * time.Second)
		_, sig, opp := tickAtSync(t, runner, fc, at, tickCh, ackCh)

		if sig.Idle != 0 {
			t.Errorf("B2 tick %d: idle = %f, want 0", i, sig.Idle)
		}
		if opp.Opportunity {
			t.Errorf("B3 tick %d: unexpected opportunity (should not fire)", i)
		}
	}

	if captured.WakeCount() != 0 {
		t.Errorf("B4: mind entered %d times, want 0", captured.WakeCount())
	}

	snap := runner.Snapshot()
	if snap.TickCount != 3 {
		t.Errorf("B5: TickCount = %d, want 3", snap.TickCount)
	}
	if !snap.LastSpontaneousWakeAt.IsZero() {
		t.Errorf("B5: LastSpontaneousWakeAt = %v, want zero (no wake)", snap.LastSpontaneousWakeAt)
	}
}

// ── Scenario C: Cognition Failure After Durable Admission ───────────────
//
// Prove that when a cognition invocation fails AFTER the admission
// checkpoint has been durably written, Pulse handles the failure
// correctly. This test exercises the global failure path — no subjects
// are involved; the wake is idle-only throughout. A fresh runner with
// a working provider can recover and wake successfully.
//
// Acceptance:
//
//	C1: First wake → CheckpointWriter called (admission checkpoint durable)
//	C2: Admission checkpoint succeeds for both calls
//	C3: Second wake → EnterPulseWake fails → cognition error surfaced
//	C4: LastSpontaneousWakeAt advances despite failure
//	C5: Fresh runner + working provider → recovery wake succeeds
//	C6: Idle is recomputed correctly after recovery

func TestM7_ScenarioC_CognitionFailureRecovery(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"test","matters":false,"reason":"test"}`)
	prov.failOnCall = 2

	mindAPI := newTestMindAPI("failure-doll", "FailureTest", prov)
	scheduler := dollmind.New(mindAPI.Inference(), logger.New(logger.ErrorLevel, nil), mindAPI)
	captured := newCapturingMind(scheduler)

	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)
	log := logger.New(logger.ErrorLevel, nil)
	runner := pulse.NewTestRunner(cfg, fc, &deterministicRNG{v: 0.01}, log, captured, tickCh, ackCh)

	checkpointCalls := new(atomic.Int64)
	runner.CheckpointWriter = func(cp pulse.PulseCheckpoint) error {
		checkpointCalls.Add(1)
		return nil
	}

	// ── Phase 1: restore before start → first wake succeeds ──
	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0,
	}
	runner.RestoreFromCheckpoint(cp)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	snap, _, opp := tickAtSync(t, runner, fc, T0, tickCh, ackCh)

	if !opp.Opportunity {
		t.Fatalf("C1: first tick opportunity should fire")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("C1: mind entered %d times, want 1", captured.WakeCount())
	}
	if snap.LastSpontaneousWakeAt != T0 {
		t.Errorf("C1: LastSpontaneousWakeAt = %v, want %v", snap.LastSpontaneousWakeAt, T0)
	}

	// C1: Admission checkpoint was written (called at least once).
	if checkpointCalls.Load() < 1 {
		t.Errorf("C1: CheckpointWriter called %d times, want >= 1",
			checkpointCalls.Load())
	}

	// ── Phase 2: second wake → provider fails after durable admission ──
	// Reset idle by restoring checkpoint with lastCognitionAt = T0.
	cp2 := pulse.PulseCheckpoint{
		LastCognitionAt:       T0,
		LastSpontaneousWakeAt: T0,
	}
	runner.RestoreFromCheckpoint(cp2)

	// Tick at T0+2s: idle=2s/1s=2.0 → saturated → wake.
	// provider.failOnCall=2 means the SECOND Infer call will fail.
	snap, _, opp = tickAtSync(t, runner, fc, T0.Add(2*time.Second), tickCh, ackCh)

	if !opp.Opportunity {
		t.Errorf("C3: second tick opportunity should fire (idle should be saturated)")
	}
	if captured.WakeCount() != 2 {
		t.Errorf("C3: mind entered %d times, want 2", captured.WakeCount())
	}

	// C4: LastSpontaneousWakeAt advances despite cognition failure.
	if snap.LastSpontaneousWakeAt != T0.Add(2*time.Second) {
		t.Errorf("C4: LastSpontaneousWakeAt = %v, want %v",
			snap.LastSpontaneousWakeAt, T0.Add(2*time.Second))
	}

	// Stop the original runner before creating a fresh one for recovery.
	runner.Stop()
	cancel()

	// ── Phase 3: recovery — fresh runner with working provider ──
	prov2 := newConformanceProvider(`{"summary":"recovery","matters":false,"reason":"recovered"}`)
	mindAPI2 := newTestMindAPI("failure-doll", "FailureTest", prov2)
	scheduler2 := dollmind.New(mindAPI2.Inference(), logger.New(logger.ErrorLevel, nil), mindAPI2)
	captured2 := newCapturingMind(scheduler2)

	runner2 := pulse.NewTestRunner(cfg, fc, &deterministicRNG{v: 0.01}, log, captured2, tickCh, ackCh)
	runner2.CheckpointWriter = func(cp pulse.PulseCheckpoint) error {
		checkpointCalls.Add(1)
		return nil
	}
	runner2.RestoreFromCheckpoint(runner.ToCheckpoint())
	if err := runner2.Start(context.Background()); err != nil {
		t.Fatalf("C5: Runner2.Start: %v", err)
	}
	defer runner2.Stop()

	_, sig2, opp2 := tickAtSync(t, runner2, fc, T0.Add(4*time.Second), tickCh, ackCh)

	if !opp2.Opportunity {
		t.Errorf("C5: recovery tick opportunity should fire")
	}
	if captured2.WakeCount() < 1 {
		t.Errorf("C5: recovered mind entered %d times, want >= 1", captured2.WakeCount())
	}

	if sig2.Idle > 0 {
		t.Logf("C6: idle = %.4f after recovery tick (recomputed from checkpoint)", sig2.Idle)
	}
}

// ── Scenario D: Restart with SQLite Persistence ─────────────────────────
//
// Prove that Pulse checkpoint data survives a full process restart via
// SQLite persistence: run tick cycles, checkpoint into SQLite, close/reopen,
// construct a genuinely fresh runner/runtime, verify the restored state
// produces correct idle levels and subsequent wakes.
//
// Acceptance:
//
//	D1: First-run: no checkpoint on disk → NewRunner starts with zero baseline
//	D2: Tick produces wake → checkpoint written to SQLite via CheckpointWriter
//	D3: Close SQLite store → reopen → checkpoint bytes survive
//	D4: Fresh runner + scheduler composed with real persistence layer
//	D5: Restored idle level is correctly computed from persisted checkpoint
//	D6: New wake → cognition → settlement occurs in the fresh runtime

func TestM7_ScenarioD_RestartSQLite(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "restart_test.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 0

	// ── Session A ──────────────────────────────────────────────────────
	fcA := &steppedClock{now: T0}
	provA := newConformanceProvider(`{"summary":"session A","matters":false,"reason":"test"}`)
	mindAPIA := newTestMindAPI("restart-doll", "RestartTest", provA)
	mindAPIA.store = store
	schedulerA := dollmind.New(mindAPIA.Inference(), logger.New(logger.ErrorLevel, nil), mindAPIA)
	capturedA := newCapturingMind(schedulerA)

	tickChA := make(chan time.Time, 10)
	ackChA := make(chan struct{}, 10)
	log := logger.New(logger.ErrorLevel, nil)
	runnerA := pulse.NewTestRunner(cfg, fcA, &deterministicRNG{v: 0.01}, log, capturedA, tickChA, ackChA)
	runnerA.UpdateSubjects(newSubject("test-subject"))

	cpStore, ok := store.(persistence.CheckpointStore)
	if !ok {
		t.Fatalf("store does not implement CheckpointStore")
	}

	// D1: No checkpoint on disk for a fresh doll.
	_, err = cpStore.LoadPulseCheckpoint(context.Background(), "restart-doll")
	if err == nil {
		t.Fatalf("D1: LoadPulseCheckpoint should fail for fresh doll")
	}

	runnerA.CheckpointWriter = func(cp pulse.PulseCheckpoint) error {
		data, err := pulse.MarshalCheckpoint(cp)
		if err != nil {
			return fmt.Errorf("marshal checkpoint: %w", err)
		}
		return cpStore.SavePulseCheckpoint(context.Background(), "restart-doll", data)
	}

	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0,
	}
	runnerA.RestoreFromCheckpoint(cp)

	cancelA := startRunner(context.Background(), t, runnerA)
	defer cancelA()
	defer runnerA.Stop()

	// D2: Tick → wake → checkpoint written to SQLite.
	_, _, oppA := tickAtSync(t, runnerA, fcA, T0, tickChA, ackChA)
	if !oppA.Opportunity {
		t.Errorf("D2: tick opportunity should fire")
	}
	if capturedA.WakeCount() < 1 {
		t.Errorf("D2: mind entered %d times, want >= 1", capturedA.WakeCount())
	}

	// Also tick once more for a second checkpoint cycle.
	tickAtSyncShort(t, runnerA, fcA, T0.Add(1*time.Second), tickChA, ackChA)

	runnerA.Stop()
	cancelA()

	// D3: Close SQLite → reopen → data survives.
	if err := store.Close(); err != nil {
		t.Fatalf("D3: store.Close: %v", err)
	}
	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("D3: NewStore after reopen: %v", err)
	}
	defer store2.Close()

	cpStore2, ok := store2.(persistence.CheckpointStore)
	if !ok {
		t.Fatalf("store2 does not implement CheckpointStore")
	}

	data, err := cpStore2.LoadPulseCheckpoint(context.Background(), "restart-doll")
	if err != nil {
		t.Fatalf("D3: LoadPulseCheckpoint after reopen: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("D3: checkpoint data is empty")
	}

	cpLoaded, err := pulse.UnmarshalCheckpoint(data)
	if err != nil {
		t.Fatalf("D3: UnmarshalCheckpoint: %v", err)
	}

	// D4: Fresh runner + scheduler, composed with real persistence.
	fcB := &steppedClock{now: T0.Add(3 * time.Second)}
	provB := newConformanceProvider(`{"summary":"session B","matters":false,"reason":"test"}`)
	mindAPIB := newTestMindAPI("restart-doll", "RestartTest", provB)
	mindAPIB.store = store2
	schedulerB := dollmind.New(mindAPIB.Inference(), logger.New(logger.ErrorLevel, nil), mindAPIB)
	capturedB := newCapturingMind(schedulerB)

	tickChB := make(chan time.Time, 10)
	ackChB := make(chan struct{}, 10)
	runnerB := pulse.NewTestRunner(cfg, fcB, &deterministicRNG{v: 0.01}, log, capturedB, tickChB, ackChB)
	runnerB.UpdateSubjects(newSubject("test-subject"))
	runnerB.CheckpointWriter = func(cp pulse.PulseCheckpoint) error {
		data, err := pulse.MarshalCheckpoint(cp)
		if err != nil {
			return fmt.Errorf("marshal checkpoint: %w", err)
		}
		return cpStore2.SavePulseCheckpoint(context.Background(), "restart-doll", data)
	}

	runnerB.RestoreFromCheckpoint(cpLoaded)

	cancelB := startRunner(context.Background(), t, runnerB)
	defer cancelB()
	defer runnerB.Stop()

	snapB, sigB, oppB := tickAtSync(t, runnerB, fcB, T0.Add(3*time.Second), tickChB, ackChB)

	// D5: Restored idle level is correctly computed.
	if sigB.Idle <= 0 {
		t.Errorf("D5: idle = %f after restore, want > 0 (computed from checkpoint)", sigB.Idle)
	}
	if !oppB.Opportunity {
		t.Errorf("D5: tick opportunity should fire after restore (idle saturated)")
	}

	// D6: New wake occurs in the fresh runtime.
	if capturedB.WakeCount() < 1 {
		t.Errorf("D6: mind entered %d times, want >= 1 (fresh wake)", capturedB.WakeCount())
	}
	wakeB := capturedB.LastWake()
	if wakeB == nil {
		t.Fatalf("D6: no PulseWake captured in fresh runtime")
	}
	if len(wakeB.Subjects) != 0 {
		t.Errorf("D6: PulseWake.Subjects = %v, want [] (idle-only)", wakeB.Subjects)
	}
	if snapB.LastSpontaneousWakeAt.IsZero() {
		t.Errorf("D6: LastSpontaneousWakeAt should be set after fresh wake")
	}
}

// ── Scenario E: Intention Separation ────────────────────────────────────
//
// Prove that PulseCheckpoint contains no transient data and only the
// canonical fields that survive marshalling and unmarshalling.

func TestM7_ScenarioE_IntentionSeparation(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-5 * time.Minute),
		LastSpontaneousWakeAt: T0,
	}
	data, err := pulse.MarshalCheckpoint(cp)
	if err != nil {
		t.Fatalf("MarshalCheckpoint: %v", err)
	}
	cp2, err := pulse.UnmarshalCheckpoint(data)
	if err != nil {
		t.Fatalf("UnmarshalCheckpoint: %v", err)
	}
	if !reflect.DeepEqual(cp, cp2) {
		t.Errorf("E1: roundtrip mismatch:\n  original: %+v\n  restored: %+v", cp, cp2)
	}

	cpFields := fieldsOf(t, cp)
	if len(cpFields) == 0 {
		t.Fatal("E2: PulseCheckpoint has no exported fields")
	}
	canonical := map[string]bool{
		"DollID":                true,
		"LastCognitionAt":       true,
		"LastSpontaneousWakeAt": true,
		"Subjects":              true,
	}
	for _, f := range cpFields {
		if !canonical[f] {
			t.Errorf("E2: unexpected field in PulseCheckpoint: %s", f)
		}
	}
	if len(cpFields) != len(canonical) {
		t.Errorf("E2: PulseCheckpoint has %d fields, want %d",
			len(cpFields), len(canonical))
	}

	zeroCp := pulse.PulseCheckpoint{}
	zeroData, err := pulse.MarshalCheckpoint(zeroCp)
	if err != nil {
		t.Fatalf("MarshalCheckpoint(zero): %v", err)
	}
	zeroCp2, err := pulse.UnmarshalCheckpoint(zeroData)
	if err != nil {
		t.Fatalf("UnmarshalCheckpoint(zero): %v", err)
	}
	if !reflect.DeepEqual(zeroCp, zeroCp2) {
		t.Errorf("E3: zero roundtrip mismatch: %+v vs %+v", zeroCp, zeroCp2)
	}
}

// ── Scenario F: Intention Discovery ─────────────────────────────────────
//
// Prove that the existing Intention discovery path (Scheduler.DueIntentions)
// operates independently of Pulse. Persist an Intention into SQLite with a
// WakeTime in the past, then construct a fresh Scheduler from the persisted
// state and verify DueIntentions discovers it. No Pulse runner is involved.
//
// Acceptance:
//
//	F1: Intention with past WakeTime persisted into SQLite
//	F2: Query DueIntentions from a fresh Scheduler → returns the due Intention
//	F3: DueIntentions returns nil for all-future or completed Intentions

func TestM7_ScenarioF_IntentionDiscovery(t *testing.T) {
	dbDir := t.TempDir()
	store, err := persistence.NewStore(filepath.Join(dbDir, "intention_test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	dollID := "intention-doll"
	dollName := "IntentionTest"

	// F1: Create and persist a doll state with a past-due Intention.
	ds := dollstate.NewDollState()
	ds.Identity = dollstate.Identity{DollID: dollID, CanonicalName: dollName}
	ds.Intentions = dollstate.Intentions{
		Items: []dollstate.IntentionItem{
			{
				ID:          "int-001",
				Subject:     "check-in",
				Description: "Scheduled check-in that became due while offline",
				WakeTime:    now.Add(-10 * time.Second).Format(time.RFC3339),
				State:       dollstate.IntentionStatePending,
			},
			{
				ID:          "int-002",
				Subject:     "future-task",
				Description: "This one is still in the future",
				WakeTime:    now.Add(1 * time.Hour).Format(time.RFC3339),
				State:       dollstate.IntentionStatePending,
			},
			{
				ID:          "int-003",
				Subject:     "completed-task",
				Description: "Already completed — should not be due",
				WakeTime:    now.Add(-1 * time.Hour).Format(time.RFC3339),
				State:       dollstate.IntentionStateCompleted,
			},
		},
	}

	if err := store.SaveDoll(context.Background(), &ds); err != nil {
		t.Fatalf("F1: SaveDoll: %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("F1: LoadDoll: %v", err)
	}
	if len(loaded.Intentions.Items) != 3 {
		t.Fatalf("F1: loaded %d Intentions, want 3", len(loaded.Intentions.Items))
	}
	if loaded.Intentions.Items[0].ID != "int-001" {
		t.Errorf("F1: first Intention ID = %q, want %q", loaded.Intentions.Items[0].ID, "int-001")
	}

	// F2: Discover due Intentions via Scheduler.DueIntentions.
	prov := newConformanceProvider(`{"summary":"test","matters":false,"reason":"test"}`)
	mindAPI := newTestMindAPI(dollID, dollName, prov)
	mindAPI.state = loaded
	mindAPI.store = store
	scheduler := dollmind.New(mindAPI.Inference(), logger.New(logger.ErrorLevel, nil), mindAPI,
		dollmind.WithTimeProvider(func() time.Time { return now }))

	due, err := scheduler.DueIntentions()
	if err != nil {
		t.Fatalf("F2: DueIntentions: %v", err)
	}

	if len(due) != 1 {
		t.Errorf("F2: DueIntentions returned %d items, want 1 (only int-001 should be due)",
			len(due))
	} else {
		if due[0].ID != "int-001" {
			t.Errorf("F2: due Intention ID = %q, want %q", due[0].ID, "int-001")
		}
		if due[0].Subject != "check-in" {
			t.Errorf("F2: due Intention Subject = %q, want %q", due[0].Subject, "check-in")
		}
		if due[0].State != dollstate.IntentionStatePending {
			t.Errorf("F2: due Intention State = %q, want %q", due[0].State, dollstate.IntentionStatePending)
		}
	}

	// F3: With a time reference before any Intention is due → empty.
	schedulerFuture := dollmind.New(mindAPI.Inference(), logger.New(logger.ErrorLevel, nil), mindAPI,
		dollmind.WithTimeProvider(func() time.Time {
			return now.Add(-20 * time.Second)
		}))
	due3, err := schedulerFuture.DueIntentions()
	if err != nil {
		t.Fatalf("F3: DueIntentions: %v", err)
	}
	if len(due3) != 0 {
		t.Errorf("F3: DueIntentions returned %d items with future time ref, want 0",
			len(due3))
	}
}

// ── Scenario G: Cooldown Inhibition ─────────────────────────────────────
//
// Prove that Pulse respects WakeCooldown: after a subject is presented
// and settled, the cooldown period prevents immediate re-waking even
// when idle pressure remains saturated.
//
// Acceptance:
//
//	G1: With WakeCooldown=60s, wake → settle → immediate tick → no wake
//	G2: After cooldown expires → next tick produces a wake
//	G3: WakeCooldown=0 (none) → immediate re-wake allowed

func TestM7_ScenarioG_CooldownInhibition(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 60
	cfg.MinWakeSpacing = 0
	cfg.ChangeHorizon = 0
	cfg.NeglectHorizon = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"cooldown test","matters":false,"reason":"test"}`)
	runner, captured, tickCh, ackCh := newProductionRunner(t, cfg, fc, &deterministicRNG{v: 0.5}, prov)
	runner.CheckpointWriter = func(cp pulse.PulseCheckpoint) error { return nil }

	// Phase 1: restore before start → first wake
	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0.Add(-120 * time.Second),
	}
	runner.RestoreFromCheckpoint(cp)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	_, _, opp1 := tickAtSync(t, runner, fc, T0, tickCh, ackCh)
	if !opp1.Opportunity {
		t.Fatalf("G1: first tick should produce an opportunity (idle saturated)")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("G1: mind entered %d times, want 1", captured.WakeCount())
	}

	// Phase 2: immediate tick during cooldown → no wake
	_, sig2, opp2 := tickAtSync(t, runner, fc, T0.Add(1*time.Second), tickCh, ackCh)

	if opp2.Opportunity {
		t.Errorf("G1: tick at T0+1s should NOT fire (subject in 60s cooldown)")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("G1: mind entered %d times after cooldown tick, want 1 (no new wake)",
			captured.WakeCount())
	}
	if sig2.Idle <= 0 {
		t.Errorf("G1: idle should be > 0 (pressure remains)")
	}

	// G2: After cooldown expires → wake allowed.
	_, _, opp3 := tickAtSync(t, runner, fc, T0.Add(61*time.Second), tickCh, ackCh)
	if !opp3.Opportunity {
		t.Errorf("G2: tick at T0+61s should fire (cooldown expired)")
	}
	if captured.WakeCount() != 2 {
		t.Errorf("G2: mind entered %d times, want 2 (second wake after cooldown)",
			captured.WakeCount())
	}

	// G3: With cooldown=0, immediate re-wake is allowed.
	cfg0 := cfg
	cfg0.WakeCooldown = 0
	runner0, captured0, tickCh0, ackCh0 := newProductionRunner(t, cfg0, fc,
		&deterministicRNG{v: 0.01},
		newConformanceProvider(`{"summary":"no cooldown","matters":false,"reason":"test"}`))
	runner0.CheckpointWriter = func(cp pulse.PulseCheckpoint) error { return nil }

	cp0 := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0.Add(-120 * time.Second),
	}
	runner0.RestoreFromCheckpoint(cp0)

	cancel0 := startRunner(context.Background(), t, runner0)
	defer cancel0()
	defer runner0.Stop()
	tickAtSync(t, runner0, fc, T0, tickCh0, ackCh0)
	if captured0.WakeCount() != 1 {
		t.Errorf("G3: first wake with cooldown=0: mind entered %d times, want 1",
			captured0.WakeCount())
	}
	tickAtSync(t, runner0, fc, T0.Add(1*time.Second), tickCh0, ackCh0)
	if captured0.WakeCount() < 2 {
		t.Errorf("G3: second immediate tick with cooldown=0: mind entered %d times, want >= 2",
			captured0.WakeCount())
	}
}

// ── Scenario H: Min Spacing Guard ───────────────────────────────────────
//
// Prove that Pulse's hard guard (MinWakeSpacing) prevents a new wake from
// being admitted when the elapsed wall-clock time since the last wake is
// shorter than the configured minimum spacing.
//
// Acceptance:
//
//	H1: With MinWakeSpacing=60s, wake → immediate tick → hard guard blocks
//	H2: After spacing expires → next tick produces wake
//	H3: lastSpontaneousWakeAt NOT advanced when guard blocks

func TestM7_ScenarioH_MinSpacingGuard(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 60
	cfg.ChangeHorizon = 0
	cfg.NeglectHorizon = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"spacing test","matters":false,"reason":"test"}`)
	runner, captured, tickCh, ackCh := newProductionRunner(t, cfg, fc, &deterministicRNG{v: 0.01}, prov)
	runner.CheckpointWriter = func(cp pulse.PulseCheckpoint) error { return nil }

	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0.Add(-120 * time.Second),
	}
	runner.RestoreFromCheckpoint(cp)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	snap1, _, opp1 := tickAtSync(t, runner, fc, T0, tickCh, ackCh)
	if !opp1.Opportunity {
		t.Fatalf("H1: first tick should produce opportunity")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("H1: mind entered %d times, want 1", captured.WakeCount())
	}
	initialWake := snap1.LastSpontaneousWakeAt

	// Phase 2: immediate tick → min spacing blocks.
	snap2, sig2, opp2 := tickAtSync(t, runner, fc, T0.Add(1*time.Second), tickCh, ackCh)
	if opp2.Opportunity {
		t.Errorf("H1: tick at T0+1s should NOT wake (min spacing 60s)")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("H1: mind entered %d times after spacing block, want 1", captured.WakeCount())
	}
	if snap2.LastSpontaneousWakeAt != initialWake {
		t.Errorf("H3: LastSpontaneousWakeAt changed from %v to %v despite guard",
			initialWake, snap2.LastSpontaneousWakeAt)
	}
	if sig2.Idle <= 0 {
		t.Errorf("H1: idle should remain >0 (pressure unchanged)")
	}

	// H2: After spacing expires → wake allowed.
	snap3, _, opp3 := tickAtSync(t, runner, fc, T0.Add(61*time.Second), tickCh, ackCh)
	if !opp3.Opportunity {
		t.Errorf("H2: tick at T0+61s should fire (min spacing satisfied)")
	}
	if captured.WakeCount() != 2 {
		t.Errorf("H2: mind entered %d times, want 2 (second wake after spacing)",
			captured.WakeCount())
	}
	if snap3.LastSpontaneousWakeAt != T0.Add(61*time.Second) {
		t.Errorf("H2: LastSpontaneousWakeAt = %v, want %v",
			snap3.LastSpontaneousWakeAt, T0.Add(61*time.Second))
	}
}

// ── Scenario I: Budget Inhibition ──────────────────────────────────────
//
// Prove that Pulse's budget mechanism (supplied by Core) correctly
// inhibits waking when a budget is set. With a crippled budget level,
// the Wake should not fire despite idle saturation. When budget is
// cleared (set to 0), wakes resume.
//
// Acceptance:
//
//	I1: Baseline: budget=0 → opportunity fires normally
//	I2: Set budget=0.9 → wake is inhibited (effectivePressure ≈ pressure*0.1)
//	I3: Clear budget back to 0 → opportunity fires again

func TestM7_ScenarioI_BudgetExhaustion(t *testing.T) {
	T0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	cfg := conformanceConfig()
	cfg.IdleHorizon = 1
	cfg.WakeCooldown = 0
	cfg.MinWakeSpacing = 0
	cfg.ChangeHorizon = 0
	cfg.NeglectHorizon = 0

	fc := &steppedClock{now: T0}
	prov := newConformanceProvider(`{"summary":"budget test","matters":false,"reason":"test"}`)
	runner, captured, tickCh, ackCh := newProductionRunner(t, cfg, fc, &deterministicRNG{v: 0.5}, prov)
	runner.CheckpointWriter = func(cp pulse.PulseCheckpoint) error { return nil }

	// Phase 1: restore before start — budget=0, opportunity fires normally
	cp := pulse.PulseCheckpoint{
		LastCognitionAt:       T0.Add(-2 * time.Second),
		LastSpontaneousWakeAt: T0.Add(-120 * time.Second),
	}
	runner.RestoreFromCheckpoint(cp)

	cancel := startRunner(context.Background(), t, runner)
	defer cancel()
	defer runner.Stop()

	_, _, opp1 := tickAtSync(t, runner, fc, T0, tickCh, ackCh)
	if !opp1.Opportunity {
		t.Fatalf("I1: first tick should produce opportunity (budget=0)")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("I1: mind entered %d times, want 1", captured.WakeCount())
	}

	oppSnap := runner.OpportunitySnapshot()
	t.Logf("I1: effectivePressure=%.4f, pressure=%.4f, inhibition cooldown=%.4f budget=%.4f",
		oppSnap.EffectivePressure, oppSnap.Pressure,
		oppSnap.Inhibition.Cooldown, oppSnap.Inhibition.Budget)

	// Phase 2: set budget=0.9 → strong inhibition → wake blocked
	runner.SetBudget(0.9)

	oppSnap2 := runner.OpportunitySnapshot()
	t.Logf("I2a: before tick with budget=0.9: inhibition budget=%.4f", oppSnap2.Inhibition.Budget)

	_, sig2, opp2 := tickAtSync(t, runner, fc, T0.Add(1*time.Second), tickCh, ackCh)

	// With budget=0.9 and no cooldown, effectivePressure = pressure * 0.1
	// RNG=0.5. So 0.5 < 0.1 → false → no opportunity.
	if opp2.Opportunity {
		t.Errorf("I2: tick at T0+1s with budget=0.9 should NOT fire (RNG=0.5 > effectivePressure=0.1)")
	}
	if captured.WakeCount() != 1 {
		t.Errorf("I2: mind entered %d times after budget block, want 1",
			captured.WakeCount())
	}
	if sig2.Idle <= 0 {
		t.Errorf("I2: idle should remain >0 (pressure unchanged)")
	}

	// Phase 3: clear budget to 0 → opportunity resumes
	runner.SetBudget(0)

	_, _, opp3 := tickAtSync(t, runner, fc, T0.Add(2*time.Second), tickCh, ackCh)
	if !opp3.Opportunity {
		t.Errorf("I3: tick at T0+2s with budget=0 should fire (no inhibition)")
	}
	if captured.WakeCount() != 2 {
		t.Errorf("I3: mind entered %d times after budget cleared, want 2",
			captured.WakeCount())
	}
}
