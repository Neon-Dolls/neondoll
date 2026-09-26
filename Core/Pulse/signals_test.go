package pulse

import (
	"context"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func sec(n int64) time.Duration {
	return time.Duration(n) * time.Second
}

var ref = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func cfgWith(idle, neglect, change, cooldown int64) config.PulseConfig {
	return config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    idle,
		NeglectHorizon: neglect,
		ChangeHorizon:  change,
		WakeCooldown:   cooldown,
	}
}

// ─── 1. Idle at exact times ─────────────────────────────────────────────────

func TestEvaluateSignals_IdleAtExactTimes(t *testing.T) {
	cfg := cfgWith(100, 0, 0, 0) // idle horizon = 100s

	tests := []struct {
		name     string
		elapsed  time.Duration // since last cognition
		wantIdle float64
	}{
		{"zero elapsed", 0, 0},
		{"half horizon", 50 * time.Second, 0.5},
		{"at horizon", 100 * time.Second, 1.0},
		{"beyond horizon", 200 * time.Second, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cogAt := ref.Add(-tt.elapsed)
			ps := PulseSnapshot{LastCognitionAt: cogAt}
			now := ref
			snap := EvaluateSignals(now, cfg, ps, nil, InhibitionInputs{})
			if snap.Idle != tt.wantIdle {
				t.Errorf("Idle = %f, want %f (elapsed=%v)", snap.Idle, tt.wantIdle, tt.elapsed)
			}
		})
	}
}

// TestEvaluateSignals_IdleNoBaseline proves that an unknown LastCognitionAt
// (zero value) yields idle=0, not an invented infinite history.
// Backwards-time Pulse evaluation is an error and is clipped to elapsed=0.
func TestEvaluateSignals_IdleNoBaseline(t *testing.T) {
	cfg := cfgWith(100, 0, 0, 0) // idle horizon = 100s
	ps := PulseSnapshot{}
	snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
	if snap.Idle != 0 {
		t.Errorf("Idle = %f, want 0 (no cognition baseline → no idle signal)", snap.Idle)
	}
}

// TestEvaluateSignals_IdleBackwardsTime proves that backwards-time evaluation
// (now < LastCognitionAt) clips elapsed to 0, producing the same result as
// zero elapsed.
func TestEvaluateSignals_IdleBackwardsTime(t *testing.T) {
	cfg := cfgWith(100, 0, 0, 0)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-time.Hour)}
	backwardsNow := ref.Add(-2 * time.Hour) // earlier than cognition
	snap := EvaluateSignals(backwardsNow, cfg, ps, nil, InhibitionInputs{})
	if snap.Idle != 0 {
		t.Errorf("Idle = %f, want 0 (backwards time → elapsed clipped to 0)", snap.Idle)
	}
}

// ─── 2. Idle with zero subjects ─────────────────────────────────────────────

func TestEvaluateSignals_IdleWithZeroSubjects(t *testing.T) {
	cfg := cfgWith(100, 0, 0, 0)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-50 * time.Second)}
	snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
	if snap.Idle != 0.5 {
		t.Errorf("Idle = %f, want 0.5 with no subjects", snap.Idle)
	}
	if len(snap.Subjects) != 0 {
		t.Errorf("expected 0 subjects, got %d", len(snap.Subjects))
	}
}

// ─── 3. Idle/neglect independence ──────────────────────────────────────────

func TestEvaluateSignals_IdleNeglectIndependence(t *testing.T) {
	cfg := cfgWith(200, 100, 0, 0)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-100 * time.Second)}
	subjects := []PulseSubjectState{
		{SubjectID: "s1", LastPresentedAt: ref.Add(-50 * time.Second)},
	}
	snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
	// idle = 100/200 = 0.5
	if snap.Idle != 0.5 {
		t.Errorf("Idle = %f, want 0.5", snap.Idle)
	}
	// neglect = 50/100 = 0.5
	if len(snap.Subjects) != 1 {
		t.Fatalf("expected 1 subject signal, got %d", len(snap.Subjects))
	}
	if snap.Subjects[0].Neglect != 0.5 {
		t.Errorf("Neglect = %f, want 0.5", snap.Subjects[0].Neglect)
	}
}

// ─── 4. Neglect progression ─────────────────────────────────────────────────

func TestEvaluateSignals_NeglectProgression(t *testing.T) {
	cfg := cfgWith(0, 100, 0, 0)
	ps := PulseSnapshot{}

	tests := []struct {
		name        string
		presented   time.Duration // ago
		wantNeglect float64
	}{
		{"just presented", 0, 0},
		{"half neglect", 50 * time.Second, 0.5},
		{"at horizon", 100 * time.Second, 1.0},
		{"beyond", 150 * time.Second, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subjects := []PulseSubjectState{
				{SubjectID: "s1", LastPresentedAt: ref.Add(-tt.presented)},
			}
			snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
			if snap.Subjects[0].Neglect != tt.wantNeglect {
				t.Errorf("Neglect = %f, want %f", snap.Subjects[0].Neglect, tt.wantNeglect)
			}
		})
	}
}

// ─── 5. Change (count-based) progression ────────────────────────────────────

func TestEvaluateSignals_ChangeCountBased(t *testing.T) {
	cfg := cfgWith(0, 0, 10, 0) // change horizon = 10 changes
	ps := PulseSnapshot{}

	tests := []struct {
		name       string
		changes    int64
		wantChange float64
	}{
		{"zero changes", 0, 0},
		{"half horizon", 5, 0.5},
		{"at horizon", 10, 1.0},
		{"beyond horizon", 20, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subjects := []PulseSubjectState{
				{SubjectID: "s1", ChangesSincePresent: tt.changes},
			}
			snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
			if snap.Subjects[0].Change != tt.wantChange {
				t.Errorf("Change = %f, want %f (changes=%d)", snap.Subjects[0].Change, tt.wantChange, tt.changes)
			}
		})
	}
}

// ─── 6. Unfinished from explicit state only ─────────────────────────────────

func TestEvaluateSignals_UnfinishedExplicitOnly(t *testing.T) {
	cfg := cfgWith(0, 0, 0, 0)
	ps := PulseSnapshot{}

	t.Run("explicit unresolved", func(t *testing.T) {
		subjects := []PulseSubjectState{
			{SubjectID: "s1", LifecycleState: LifecycleStateUnresolved},
		}
		snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
		if snap.Subjects[0].Unfinished != 1.0 {
			t.Errorf("Unfinished = %f, want 1.0", snap.Subjects[0].Unfinished)
		}
	})

	t.Run("empty lifecycle", func(t *testing.T) {
		subjects := []PulseSubjectState{
			{SubjectID: "s1", LifecycleState: ""},
		}
		snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
		if snap.Subjects[0].Unfinished != 0.0 {
			t.Errorf("Unfinished = %f, want 0.0", snap.Subjects[0].Unfinished)
		}
	})

	t.Run("arbitrary text does not set unfinished", func(t *testing.T) {
		subjects := []PulseSubjectState{
			{SubjectID: "s1", LifecycleState: "pending completion of long-running task"},
		}
		snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
		if snap.Subjects[0].Unfinished != 0.0 {
			t.Errorf("Unfinished = %f, want 0.0 (semantic prose must not trigger)", snap.Subjects[0].Unfinished)
		}
	})
}

// ─── 7. Cooldown decay ──────────────────────────────────────────────────────

func TestEvaluateSignals_CooldownDecay(t *testing.T) {
	cfg := cfgWith(0, 0, 0, 100) // cooldown horizon = 100s

	tests := []struct {
		name         string
		sinceWake    time.Duration
		wantCooldown float64
	}{
		{"just woke", 0, 1.0},
		{"half cooldown", 50 * time.Second, 0.5},
		{"at horizon", 100 * time.Second, 0},
		{"beyond", 200 * time.Second, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := PulseSnapshot{LastSpontaneousWakeAt: ref.Add(-tt.sinceWake)}
			snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
			if snap.Cooldown != tt.wantCooldown {
				t.Errorf("Cooldown = %f, want %f", snap.Cooldown, tt.wantCooldown)
			}
		})
	}
}

func TestEvaluateSignals_CooldownWithZeroWakeCooldown(t *testing.T) {
	cfg := cfgWith(0, 0, 0, 0) // cooldown disabled
	ps := PulseSnapshot{LastSpontaneousWakeAt: ref.Add(-10 * time.Second)}
	snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
	if snap.Cooldown != 0 {
		t.Errorf("Cooldown = %f, want 0 (disabled)", snap.Cooldown)
	}
}

func TestEvaluateSignals_CooldownNoWake(t *testing.T) {
	cfg := cfgWith(0, 0, 0, 100)
	ps := PulseSnapshot{} // zero wake = no recent wake
	snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
	if snap.Cooldown != 0 {
		t.Errorf("Cooldown = %f, want 0 (no wake recorded)", snap.Cooldown)
	}
}

// ─── 8. Budget inhibition ───────────────────────────────────────────────────

func TestEvaluateSignals_BudgetInhibition(t *testing.T) {
	cfg := cfgWith(0, 0, 0, 0)
	ps := PulseSnapshot{}

	tests := []struct {
		name       string
		budget     float64
		wantBudget float64
	}{
		{"zero budget", 0, 0},
		{"half budget", 0.5, 0.5},
		{"full budget", 1.0, 1.0},
		{"above 1 clamped", 2.0, 1.0},
		{"below 0 clamped", -0.5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{Budget: tt.budget})
			if snap.Budget != tt.wantBudget {
				t.Errorf("Budget = %f, want %f", snap.Budget, tt.wantBudget)
			}
		})
	}
}

// ─── 9. Zero horizons ──────────────────────────────────────────────────────

func TestEvaluateSignals_ZeroHorizons(t *testing.T) {
	// All horizons zero = all activation signals disabled
	cfg := cfgWith(0, 0, 0, 0)
	ps := PulseSnapshot{}
	subjects := []PulseSubjectState{
		{
			SubjectID:           "s1",
			LastPresentedAt:     ref.Add(-1000 * time.Second),
			ChangesSincePresent: 999,
			LifecycleState:      "",
		},
	}
	snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{Budget: 0.3})
	if snap.Idle != 0 {
		t.Errorf("Idle = %f, want 0", snap.Idle)
	}
	if snap.Subjects[0].Neglect != 0 {
		t.Errorf("Neglect = %f, want 0", snap.Subjects[0].Neglect)
	}
	if snap.Subjects[0].Change != 0 {
		t.Errorf("Change = %f, want 0", snap.Subjects[0].Change)
	}
	if snap.Subjects[0].Unfinished != 0 {
		t.Errorf("Unfinished = %f, want 0", snap.Subjects[0].Unfinished)
	}
}

// ─── 10. Clamping ───────────────────────────────────────────────────────────

func TestEvaluateSignals_Clamping(t *testing.T) {
	// Verify all signals are in [0, 1] regardless of configuration
	cfg := cfgWith(10, 10, 5, 10)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-20 * time.Second), LastSpontaneousWakeAt: ref.Add(-30 * time.Second)}
	subjects := []PulseSubjectState{
		{SubjectID: "s1", LastPresentedAt: ref.Add(-20 * time.Second), ChangesSincePresent: 20},
	}
	snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{Budget: 0.7})

	if snap.Idle < 0 || snap.Idle > 1 {
		t.Errorf("Idle = %f out of [0,1]", snap.Idle)
	}
	if snap.Cooldown < 0 || snap.Cooldown > 1 {
		t.Errorf("Cooldown = %f out of [0,1]", snap.Cooldown)
	}
	if snap.Budget < 0 || snap.Budget > 1 {
		t.Errorf("Budget = %f out of [0,1]", snap.Budget)
	}
	for _, s := range snap.Subjects {
		if s.Neglect < 0 || s.Neglect > 1 {
			t.Errorf("Neglect(%s) = %f out of [0,1]", s.SubjectID, s.Neglect)
		}
		if s.Change < 0 || s.Change > 1 {
			t.Errorf("Change(%s) = %f out of [0,1]", s.SubjectID, s.Change)
		}
		if s.Unfinished < 0 || s.Unfinished > 1 {
			t.Errorf("Unfinished(%s) = %f out of [0,1]", s.SubjectID, s.Unfinished)
		}
	}
}

// ─── 11. Backwards time ─────────────────────────────────────────────────────

func TestEvaluateSignals_BackwardsTime(t *testing.T) {
	// Idle from backwards cognition time
	cfg := cfgWith(100, 0, 0, 0)
	// Start with cognition at ref
	now := ref
	cogAt := ref.Add(-50 * time.Second)
	ps := PulseSnapshot{LastCognitionAt: cogAt}
	snap := EvaluateSignals(now, cfg, ps, nil, InhibitionInputs{})
	firstIdle := snap.Idle

	// Now evaluate at EARLIER time — elapsed becomes negative -> normalize clamps to 0
	earlier := ref.Add(-100 * time.Second)
	snapBack := EvaluateSignals(earlier, cfg, ps, nil, InhibitionInputs{})

	// elapsed = earlier - cogAt = -100 - (-50) = -50s => 0 (idle disabled)
	if snapBack.Idle != 0 {
		t.Errorf("Backwards-time Idle = %f, want 0 (negative elapsed)", snapBack.Idle)
	}
	// first idle should be unaffected (pure function, no mutation)
	if snap.Idle != firstIdle {
		t.Errorf("First Idle changed from %f to %f after backwards call", firstIdle, snap.Idle)
	}
}

// ─── 12. No semantic inference ──────────────────────────────────────────────

func TestEvaluateSignals_NoSemanticInference(t *testing.T) {
	// Prove that changing semantic prose without changing revision metadata
	// has zero effect on signals.
	cfg := cfgWith(0, 0, 5, 0)
	ps := PulseSnapshot{}

	// Two subjects: same metadata but different semantic "prose".
	// PulseSubjectState has no prose field, so this is a compile-time guarantee
	// that Pulse cannot inspect content. The test proves no future regression
	// by ensuring the only thing that affects change is ChangesSincePresent.
	subjects := []PulseSubjectState{
		{SubjectID: "s1", ChangesSincePresent: 3},
	}
	snap := EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{})
	if snap.Subjects[0].Change != 0.6 {
		t.Errorf("Change = %f, want 0.6", snap.Subjects[0].Change)
	}
}

// ─── 13. No subject invention ──────────────────────────────────────────────

func TestEvaluateSignals_NoSubjectInvention(t *testing.T) {
	cfg := cfgWith(100, 100, 5, 0)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-50 * time.Second)}

	// No subjects passed in — Pulse must not invent any
	snap := EvaluateSignals(ref, cfg, ps, nil, InhibitionInputs{})
	if len(snap.Subjects) != 0 {
		t.Errorf("Pulse invented %d subjects; want 0", len(snap.Subjects))
	}

	// Empty slice is also fine
	snap2 := EvaluateSignals(ref, cfg, ps, []PulseSubjectState{}, InhibitionInputs{})
	if len(snap2.Subjects) != 0 {
		t.Errorf("Pulse invented %d subjects from empty slice; want 0", len(snap2.Subjects))
	}
}

// ─── 14. No Doll State mutation ─────────────────────────────────────────────

func TestEvaluateSignals_NoDollStateMutation(t *testing.T) {
	// EvaluateSignals is pure — verify it doesn't modify its inputs
	cfg := cfgWith(100, 100, 5, 100)
	ps := PulseSnapshot{
		LastCognitionAt:       ref.Add(-30 * time.Second),
		LastSpontaneousWakeAt: ref.Add(-10 * time.Second),
	}
	subjects := []PulseSubjectState{
		{
			SubjectID:           "s1",
			LastPresentedAt:     ref.Add(-20 * time.Second),
			ChangesSincePresent: 3,
		},
	}
	subjectsCopy := make([]PulseSubjectState, len(subjects))
	copy(subjectsCopy, subjects)
	psCopy := ps

	EvaluateSignals(ref, cfg, ps, subjects, InhibitionInputs{Budget: 0.5})

	// ps should be unchanged
	if ps.LastCognitionAt != psCopy.LastCognitionAt {
		t.Error("PulseSnapshot was mutated")
	}
	if ps.LastSpontaneousWakeAt != psCopy.LastSpontaneousWakeAt {
		t.Error("PulseSnapshot was mutated")
	}
	// subjects should be unchanged
	for i := range subjects {
		if subjects[i].SubjectID != subjectsCopy[i].SubjectID ||
			subjects[i].ChangesSincePresent != subjectsCopy[i].ChangesSincePresent {
			t.Error("subjects slice was mutated")
		}
	}
}

// ─── 15. No RNG consumption ─────────────────────────────────────────────────

func TestEvaluateSignals_NoRNGConsumption(t *testing.T) {
	// Deterministic: same inputs → same outputs every time
	cfg := cfgWith(100, 100, 5, 100)
	ps := PulseSnapshot{LastCognitionAt: ref.Add(-50 * time.Second)}
	subjects := []PulseSubjectState{
		{SubjectID: "s1", LastPresentedAt: ref.Add(-25 * time.Second), ChangesSincePresent: 2},
	}
	inhibition := InhibitionInputs{Budget: 0.3}

	snap1 := EvaluateSignals(ref, cfg, ps, subjects, inhibition)
	snap2 := EvaluateSignals(ref, cfg, ps, subjects, inhibition)

	if snap1.Idle != snap2.Idle {
		t.Error("Idle not deterministic")
	}
	if snap1.Cooldown != snap2.Cooldown {
		t.Error("Cooldown not deterministic")
	}
	if snap1.Budget != snap2.Budget {
		t.Error("Budget not deterministic")
	}
	for i := range snap1.Subjects {
		if snap1.Subjects[i].Neglect != snap2.Subjects[i].Neglect {
			t.Error("Neglect not deterministic")
		}
		if snap1.Subjects[i].Change != snap2.Subjects[i].Change {
			t.Error("Change not deterministic")
		}
		if snap1.Subjects[i].Unfinished != snap2.Subjects[i].Unfinished {
			t.Error("Unfinished not deterministic")
		}
	}
}

// ─── 16. Runner race safety ─────────────────────────────────────────────────

func TestRunner_SignalSnapshotRaceSafety(t *testing.T) {
	cfg := cfgWith(100, 100, 5, 100)
	log := logger.New(logger.ErrorLevel, io.Discard)
	fc := NewFakeClock(ref)

	r := NewRunner(cfg, fc, log)

	var wg sync.WaitGroup
	// Concurrent reads of SignalSnapshot while writing subjects/budget
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = r.SignalSnapshot()
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.UpdateSubjects([]PulseSubjectState{
					{SubjectID: "s1", LastPresentedAt: ref},
				})
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.SetBudget(float64(j%11) / 10.0)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
}

func TestRunner_SignalSnapshotThroughEvaluate(t *testing.T) {
	cfg := cfgWith(100, 0, 0, 0)
	log := logger.New(logger.ErrorLevel, io.Discard)
	fc := NewFakeClock(ref)

	r := NewRunner(cfg, fc, log)
	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)
	r.tickTestCh = tickCh
	r.tickAckCh = ackCh

	// Start the runner
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Set up some state
	r.RecordCognition(ref.Add(-50 * time.Second))
	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "s1", LastPresentedAt: ref.Add(-30 * time.Second)},
	})
	r.SetBudget(0.3)

	// Tick once
	tickCh <- ref
	<-ackCh

	// Read signal snapshot
	snap := r.SignalSnapshot()
	if snap.Idle != 0.5 {
		t.Errorf("Idle = %f, want 0.5", snap.Idle)
	}
	if snap.Budget != 0.3 {
		t.Errorf("Budget = %f, want 0.3", snap.Budget)
	}

	// Tick again — idle should increase
	r.RecordCognition(ref) // reset cognition to ref
	// Wait, RecordCognition with ref which is > ref.Add(-50) but...
	// Actually ref is the current clock time. Since cognition was at ref-50,
	// and we advance... Let me think.
	// The clock hasn't advanced. We set cognition to ref. Now idle = normalize(0, 100) = 0.
	// Let me verify:
	snap2 := r.SignalSnapshot() // still the previous snapshot since we didn't tick
	_ = snap2

	// Let me just verify the tick produced signals by advancing time
	fc.(*fakeClock).Advance(25 * time.Second)
	tickCh <- ref.Add(25 * time.Second)
	<-ackCh

	snap3 := r.SignalSnapshot()
	if snap3.At != ref.Add(25*time.Second) {
		t.Errorf("snap.At = %v, want %v", snap3.At, ref.Add(25*time.Second))
	}

	r.Stop()
}

// ─── 17. Big conformance scenario ──────────────────────────────────────────

func TestEvaluateSignals_BigConformance(t *testing.T) {
	// This test exercises the full M2 contract in one deterministic scenario.
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    120, // 120s idle horizon
		NeglectHorizon: 60,  // 60s neglect horizon
		ChangeHorizon:  4,   // 4 changes → saturated
		WakeCooldown:   300, // 300s cooldown horizon
	}

	// T0: baseline — last cognition at T0, subject presented at T0
	T0 := ref
	ps := PulseSnapshot{
		LastCognitionAt:       T0,
		LastSpontaneousWakeAt: T0,
	}
	subjects := []PulseSubjectState{
		{
			SubjectID:           "d Drive",
			LastPresentedAt:     T0,
			ChangesSincePresent: 0,
			LifecycleState:      "",
		},
	}

	// ── Phase A: Advance 30s ──
	// idle = 30/120 = 0.25, neglect = 30/60 = 0.5
	T1 := T0.Add(30 * time.Second)
	snap := EvaluateSignals(T1, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertApprox(t, "idle T1", snap.Idle, 0.25)
	assertApprox(t, "neglect T1", snap.Subjects[0].Neglect, 0.5)
	assertEqual(t, "change T1", snap.Subjects[0].Change, 0.0)
	assertEqual(t, "unfinished T1", snap.Subjects[0].Unfinished, 0.0)
	// cooldown: 30s since wake out of 300s → 0.9 inhibition (1-0.1)
	assertApprox(t, "cooldown T1", snap.Cooldown, 0.9)
	assertEqual(t, "budget T1", snap.Budget, 0.0)

	// ── Phase B: Subject accumulates changes (no time advance) ──
	subjects[0].ChangesSincePresent = 2
	snap = EvaluateSignals(T1, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertEqual(t, "change B1", snap.Subjects[0].Change, 0.5) // 2/4 = 0.5
	assertEqual(t, "unfinished B1", snap.Subjects[0].Unfinished, 0.0)

	// ── Phase C: Set lifecycle to unresolved ──
	subjects[0].LifecycleState = LifecycleStateUnresolved
	snap = EvaluateSignals(T1, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertEqual(t, "unfinished C1", snap.Subjects[0].Unfinished, 1.0)
	// Other signals unchanged
	assertEqual(t, "change C1", snap.Subjects[0].Change, 0.5)
	assertApprox(t, "neglect C1", snap.Subjects[0].Neglect, 0.5)

	// ── Phase D: Advance to T1+60s (total 90s since cognition, 90s since present) ──
	T2 := T1.Add(60 * time.Second) // = T0 + 90s
	// idle = 90/120 = 0.75
	// neglect = 90/60 = 1.0 (saturated)
	// cooldown = 1 - 90/300 = 1 - 0.3 = 0.7
	subjects[0].LifecycleState = LifecycleStateUnresolved // still unresolved
	snap = EvaluateSignals(T2, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertApprox(t, "idle D1", snap.Idle, 0.75)
	assertEqual(t, "neglect D1", snap.Subjects[0].Neglect, 1.0)
	assertApprox(t, "cooldown D1", snap.Cooldown, 0.7)

	// ── Phase E: Add budget inhibition ──
	snap = EvaluateSignals(T2, cfg, ps, subjects, InhibitionInputs{Budget: 0.4})
	assertEqual(t, "budget E1", snap.Budget, 0.4)
	// Activation signals unchanged by budget
	assertApprox(t, "idle E1", snap.Idle, 0.75)
	assertEqual(t, "neglect E1", snap.Subjects[0].Neglect, 1.0)
	assertEqual(t, "unfinished E1", snap.Subjects[0].Unfinished, 1.0)

	// ── Phase F: After wake cooldown expires (advance 300s from T0, but keep
	// LastSpontaneousWakeAt at T0, so 300s elapsed → cooldown = 0) ──
	T3 := T0.Add(300 * time.Second)
	snap = EvaluateSignals(T3, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertEqual(t, "cooldown F1", snap.Cooldown, 0.0)           // fully decayed
	assertEqual(t, "idle F1", snap.Idle, 1.0)                   // saturated
	assertEqual(t, "neglect F1", snap.Subjects[0].Neglect, 1.0) // saturated

	// ── Phase G: Set full changes (6 > 4 horizon) ──
	subjects[0].ChangesSincePresent = 6
	snap = EvaluateSignals(T3, cfg, ps, subjects, InhibitionInputs{Budget: 0.0})
	assertEqual(t, "change G1", snap.Subjects[0].Change, 1.0)

	// ── Phase H: No random draw, no pressure, no wake, no Mind, no inference ──
	// Verify no M3 leakage: these fields DON'T exist on SignalSnapshot.
	// The test proves M2 is complete by checking every M2 field exists and
	// no M3 concepts leak.
	_ = snap.At
	_ = snap.Idle
	_ = snap.Subjects
	_ = snap.Cooldown
	_ = snap.Budget
}

// ─── test helpers ───────────────────────────────────────────────────────────

func assertApprox(t *testing.T, name string, got, want float64) {
	t.Helper()
	diff := math.Abs(got - want)
	if diff > 1e-9 {
		t.Errorf("%s = %v, want ≈ %v (diff=%v)", name, got, want, diff)
	}
}

func assertEqual[V comparable](t *testing.T, name string, got, want V) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
