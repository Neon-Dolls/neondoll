package pulse

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
)

// ---------------------------------------------------------------------------
// M5: Deterministic Extended Trace — Pulse Settles and Continues
// ---------------------------------------------------------------------------

// TestM5_MultiplePulseCycles demonstrates several complete Pulse cycles:
// pressure rises → spontaneous wake → cognition → settling → idle resets.
// This is the comprehensive "example timeline" from the M5 spec.
func TestM5_MultiplePulseCycles(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    300, // 5 min idle horizon
		NeglectHorizon: 600, // 10 min neglect horizon
		ChangeHorizon:  300, // 5 min change horizon
		WakeCooldown:   300, // 5 min cooldown
		MinWakeSpacing: 120, // 2 min spacing
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01, 0.01, 0.01) // 3 eligible opportunities
	mind := &testMindEntry{}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	// Subject is unresolved — contributes unfinished=1.0 so it always
	// appears in the opportunity's subject activation list.
	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-1", LifecycleState: LifecycleStateUnresolved},
	})

	// Seed an initial cognition baseline so idle starts ticking.
	r.RecordCognition(startAt)

	// ---- Cycle 1: T+5m (09:05) ----
	// Idle has been rising for 5 min → saturated at 1.0 (300/300).
	// Unfinished=1.0 → total pressure ≈ 1.0.
	// No cooldown (no spontaneous wake yet), no MinWakeSpacing guard.
	// Effective pressure ≈ 1.0 >> 0.01 → opportunity.
	clock.Set(startAt.Add(5 * time.Minute))
	r.evaluate()
	if !r.OpportunitySnapshot().Opportunity {
		t.Fatal("expected opportunity at T+5m (cycle 1)")
	}
	if mind.entered == 0 {
		t.Fatal("expected mind entry on first cycle")
	}
	// Subjects presented and settled at 09:05.
	assertSubjectSettled(t, r, "subj-1", startAt.Add(5*time.Minute))

	snap := r.Snapshot()
	if !snap.LastCognitionAt.Equal(startAt.Add(5 * time.Minute)) {
		t.Fatalf("expected LastCognitionAt 09:05, got %v", snap.LastCognitionAt)
	}
	if !snap.LastSpontaneousWakeAt.Equal(startAt.Add(5 * time.Minute)) {
		t.Fatalf("expected LastSpontaneousWakeAt 09:05, got %v", snap.LastSpontaneousWakeAt)
	}

	// ---- T+6m (09:06): inside MinWakeSpacing (120s) ----
	// Elapsed since last wake = 60s < 120s → guard blocks.
	clock.Set(startAt.Add(6 * time.Minute))
	r.evaluate()
	opp := r.OpportunitySnapshot()
	if opp.Opportunity {
		t.Fatal("expected no opportunity within MinWakeSpacing at T+6m")
	}
	found := false
	for _, g := range opp.Guards {
		if g.Reason == GuardReasonMinWakeSpacing && g.Blocked {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected MinWakeSpacing guard to block at T+6m")
	}

	// ---- T+8m12s (09:08:12): Past MinWakeSpacing, cooldown still active ----
	// Elapsed since wake = 3m12s = 192s > 120s → spacing satisfied.
	// Cooldown = 1 - normalize(192,300) = 0.36 → effectivePressure < pressure.
	clock.Set(startAt.Add(8*time.Minute + 12*time.Second))
	r.evaluate()
	opp2 := r.OpportunitySnapshot()
	if !opp2.Opportunity {
		t.Fatal("expected opportunity after MinWakeSpacing at T+8m12s (cycle 2)")
	}
	if opp2.Inhibition.Cooldown <= 0 {
		t.Fatal("expected positive cooldown at T+8m12s")
	}
	if opp2.EffectivePressure >= opp2.Pressure {
		t.Fatal("expected EffectivePressure < Pressure due to cooldown")
	}
	// Second mind entry occurred.
	if mind.entered < 2 {
		t.Fatalf("expected at least 2 mind entries by T+8m12s, got %d", mind.entered)
	}
	// Subject settled at the second cycle's time.
	assertSubjectSettled(t, r, "subj-1", startAt.Add(8*time.Minute+12*time.Second))

	// ---- T+16m (09:16): Third cycle check — well past MinWakeSpacing (2m) ----
	// Elapsed since cycle 2 wake at 09:08:12 = 7m48s
	clock.Set(startAt.Add(16 * time.Minute))
	r.evaluate()
	opp3 := r.OpportunitySnapshot()
	if !opp3.Opportunity {
		t.Fatal("expected opportunity at T+16m (cycle 3)")
	}
	// Subject settled a third time.
	assertSubjectSettled(t, r, "subj-1", startAt.Add(16*time.Minute))

	t.Logf("M5 trace: cycle1 pressure=%.2f eff=%.2f cooldown=%.2f, cycle2 pressure=%.2f eff=%.2f cooldown=%.2f, cycle3 pressure=%.2f eff=%.2f cooldown=%.2f",
		r.OpportunitySnapshot().Pressure, r.OpportunitySnapshot().EffectivePressure, r.OpportunitySnapshot().Inhibition.Cooldown,
		opp2.Pressure, opp2.EffectivePressure, opp2.Inhibition.Cooldown,
		opp3.Pressure, opp3.EffectivePressure, opp3.Inhibition.Cooldown)

	t.Log("M5 MultiplePulseCycles: all assertions passed")
}

// TestM5_FailedCognition verifies that a failed EnterPulseWake marks subjects
// as presented but NOT as successfully settled.
func TestM5_FailedCognition(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    60,
		NeglectHorizon: 600,
		WakeCooldown:   0,
		MinWakeSpacing: 0,
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01)
	mind := &testMindEntry{returnError: errors.New("inference failure")}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-fail", LifecycleState: LifecycleStateUnresolved},
	})
	r.RecordCognition(startAt)

	// Advance 1 min so idle = 1.0 → opportunity → failed cognition.
	clock.Set(startAt.Add(1 * time.Minute))
	r.evaluate()

	if !r.OpportunitySnapshot().Opportunity {
		t.Fatal("expected opportunity for failed cognition test")
	}
	if mind.entered != 1 {
		t.Fatalf("expected 1 mind entry, got %d", mind.entered)
	}

	// Subject should be presented (cognition began) but NOT settled.
	subjs := r.SubjectSnapshots()
	var found bool
	for _, subj := range subjs {
		if subj.SubjectID == "subj-fail" {
			found = true
			if subj.LastPresentedAt.IsZero() {
				t.Fatal("expected subject to be marked presented after failed cognition")
			}
			if !subj.LastPresentedAt.Equal(startAt.Add(1 * time.Minute)) {
				t.Fatalf("expected LastPresentedAt=T+1m, got %v", subj.LastPresentedAt)
			}
			if !subj.LastSettledAt.IsZero() {
				t.Fatal("expected subject to NOT be settled after failed cognition")
			}
		}
	}
	if !found {
		t.Fatal("subject not found in snapshots")
	}

	// Global bookkeeping is still updated even on failure.
	snap := r.Snapshot()
	if snap.LastCognitionAt.Before(startAt.Add(1 * time.Minute)) {
		t.Fatal("expected LastCognitionAt to be updated on failed cognition")
	}
	if snap.LastSpontaneousWakeAt.Before(startAt.Add(1 * time.Minute)) {
		t.Fatal("expected LastSpontaneousWakeAt to be updated on failed cognition")
	}

	t.Log("M5 FailedCognition: presented but NOT settled — verified")
}

// TestM5_CooldownAffectsOpportunity verifies that cooldown from a recent
// spontaneous wake reduces effective pressure enough to block a subsequent
// optional opportunity, and that after the WakeCooldown window expires the
// opportunity returns.
func TestM5_CooldownAffectsOpportunity(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    60,  // idle saturates in 1 min
		NeglectHorizon: 60,  // neglect saturates in 1 min
		WakeCooldown:   120, // 2 min cooldown window
		MinWakeSpacing: 0,   // disable spacing to isolate cooldown effect
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01, 0.01, 0.01)
	mind := &testMindEntry{}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-cd", LifecycleState: LifecycleStateUnresolved},
	})
	r.RecordCognition(startAt)

	// T+60s: idle = 1.0, unfinished = 1.0, pressure ≈ 1.0, eff ≈ 1.0
	// → opportunity → cognition succeeds.
	clock.Set(startAt.Add(60 * time.Second))
	r.evaluate()
	if !r.OpportunitySnapshot().Opportunity {
		t.Fatal("expected opportunity at T+60s (pre-cooldown)")
	}
	if mind.entered != 1 {
		t.Fatalf("expected 1 mind entry by T+60s, got %d", mind.entered)
	}

	// Now the subject is presented+sand settled, so neglect is based on
	// LastPresentedAt = T+60s. Unfinished is 0 (no longer unresolved).
	// We need unfinished to remain non-zero to test cooldown isolation.
	// Force the subject back to unresolved to keep a signal for the cooldown test.
	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-cd", LifecycleState: LifecycleStateUnresolved, LastPresentedAt: startAt.Add(60 * time.Second), LastSettledAt: startAt.Add(60 * time.Second)},
	})

	// T+63s: 3 seconds after wake. Cooldown is near-1.
	// idle ≈ normalize(3, 60) = 0.05, unfinished = 1.0
	// pressure ≈ 1.0, effPressure ≈ 1.0 * (1-0.975) = 0.025
	// With rng sample=0.01: 0.01 < 0.025 → should OPPORTUNITY since effPressure > 0.01!
	// Hmm, we want cooldown to block. Need lower effPressure.
	// Let me come back sooner: T+61s.
	clock.Set(startAt.Add(61 * time.Second))
	r.evaluate()
	oppClose := r.OpportunitySnapshot()
	// With T+61s: elapsed = 1s, cooldown = 1 - normalize(1,120) = 1 - 0.00833 = 0.9917
	// idle = normalize(1,60) = 0.0167, unfinished = 1.0
	// pressure = 1 - (1-0.0167)*(1-1.0) = 1 - 0.9833*0 = 1.0 (saturated by unfinished)
	// effective = 1.0 * 0.0083 = 0.0083
	// With rng sample = 0.01: 0.01 < 0.0083 = false → blocked by cooldown!
	if oppClose.Opportunity {
		t.Fatal("expected cooldown to block opportunity at T+61s")
	}
	if oppClose.Inhibition.Cooldown < 0.9 {
		t.Fatalf("expected cooldown > 0.9 at T+61s, got %f", oppClose.Inhibition.Cooldown)
	}
	t.Logf("T+61s: pressure=%.4f effective=%.4f cooldown=%.4f — blocked as expected",
		oppClose.Pressure, oppClose.EffectivePressure, oppClose.Inhibition.Cooldown)

	// T+180s (120s after wake): WakeCooldown expires.
	// Cooldown should be 0 (1 - normalize(120,120) = 0).
	clock.Set(startAt.Add(180 * time.Second))
	r.evaluate()
	oppExpired := r.OpportunitySnapshot()
	if !oppExpired.Opportunity {
		t.Fatalf("expected opportunity after WakeCooldown expired at T+180s (effP=%f)", oppExpired.EffectivePressure)
	}
	if oppExpired.Inhibition.Cooldown > 0 {
		t.Fatalf("expected cooldown = 0 at T+180s, got %f", oppExpired.Inhibition.Cooldown)
	}

	t.Log("M5 CooldownAffectsOpportunity: verified")
}

// TestM5_MinSpacingBlocksRepeatWake verifies that MinWakeSpacing prevents a
// pathological second spontaneous wake immediately after a previous one.
func TestM5_MinSpacingBlocksRepeatWake(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    60,
		NeglectHorizon: 600,
		WakeCooldown:   0, // disable cooldown to isolate spacing
		MinWakeSpacing: 120,
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01, 0.01, 0.01)
	mind := &testMindEntry{}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-spacing", LifecycleState: LifecycleStateUnresolved},
	})
	r.RecordCognition(startAt)

	// T+60s: idle = 1.0, opportunity → cognition succeeds.
	clock.Set(startAt.Add(60 * time.Second))
	r.evaluate()
	if !r.OpportunitySnapshot().Opportunity {
		t.Fatal("expected opportunity at T+60s")
	}
	if mind.entered != 1 {
		t.Fatalf("expected 1 mind entry by T+60s, got %d", mind.entered)
	}

	// T+119s: 59s after wake, still within 120s spacing → guard blocks.
	// eligible = false → no RNG sample consumed.
	clock.Set(startAt.Add(119 * time.Second))
	r.evaluate()
	oppBlocked := r.OpportunitySnapshot()
	if oppBlocked.Opportunity {
		t.Fatal("expected MinWakeSpacing to block opportunity at T+119s")
	}
	foundSpacingGuard := false
	for _, g := range oppBlocked.Guards {
		if g.Reason == GuardReasonMinWakeSpacing && g.Blocked {
			foundSpacingGuard = true
			break
		}
	}
	if !foundSpacingGuard {
		t.Fatal("expected MinWakeSpacing guard with Blocked=true at T+119s")
	}

	// T+181s: 121s after wake, past MinWakeSpacing → opportunity possible.
	clock.Set(startAt.Add(181 * time.Second))
	r.evaluate()
	oppAllowed := r.OpportunitySnapshot()
	if !oppAllowed.Opportunity {
		t.Fatal("expected opportunity after MinWakeSpacing elapsed at T+181s")
	}

	t.Log("M5 MinSpacingBlocksRepeatWake: verified")
}

// TestM5_NoOverlappingCognition verifies that Pulse does not start a new
// spontaneous cognition run while one is already active.
func TestM5_NoOverlappingCognition(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    60,
		NeglectHorizon: 600,
		WakeCooldown:   0,
		MinWakeSpacing: 0,
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01)
	mind := &testMindEntry{blockCh: make(chan struct{})}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-overlap", LifecycleState: LifecycleStateUnresolved},
	})
	r.RecordCognition(startAt)

	// T+60s: idle = 1.0 → opportunity → mind entry blocks.
	clock.Set(startAt.Add(60 * time.Second))
	go r.evaluate()

	// Wait until cognition run is claimed (blocking in EnterPulseWake).
	for i := 0; i < 100; i++ {
		if atomic.LoadInt32(&r.cognitionRun) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(&r.cognitionRun) != 1 {
		t.Fatal("expected cognition run to be active (blocking in mind entry)")
	}

	// T+65s: second evaluate while first cognition run still blocked.
	// The cognition-run guard should block opportunity.
	clock.Set(startAt.Add(65 * time.Second))
	r.evaluate()
	oppDuring := r.OpportunitySnapshot()
	if oppDuring.Opportunity {
		t.Fatal("expected cognition-run guard to block overlapping opportunity")
	}
	foundCognGuard := false
	for _, g := range oppDuring.Guards {
		if g.Reason == GuardReasonCognitionRun && g.Blocked {
			foundCognGuard = true
			break
		}
	}
	if !foundCognGuard {
		t.Fatal("expected cognition-run guard with Blocked=true")
	}

	// Unblock the first cognition run and let it complete.
	close(mind.blockCh)
	time.Sleep(10 * time.Millisecond)

	// After the first run completes, the subject should be presented and settled.
	assertSubjectSettled(t, r, "subj-overlap", startAt.Add(60*time.Second))

	t.Log("M5 NoOverlappingCognition: verified")
}

// TestM5_HardObligationsNotBlocked verifies that an externally-admitted
// cognition run (a "hard obligation") is NOT blocked by Pulse's optional-wake
// guards such as MinWakeSpacing.
//
// After a Pulse spontaneous wake completes, MinWakeSpacing would block another
// Pulse spontaneous wake. But an external caller can still call
// MindEntrance.EnterPulseWake directly — it bypasses Pulse's evaluation.
func TestM5_HardObligationsNotBlocked(t *testing.T) {
	startAt := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    60,
		NeglectHorizon: 600,
		WakeCooldown:   0,
		MinWakeSpacing: 120, // 2 min spacing — will block Pulse spontaneous wakes
	}

	clock := NewFakeClock(startAt).(*fakeClock)
	rng := newFakeRNG(0.01)
	mind := &testMindEntry{}
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	r.UpdateSubjects([]PulseSubjectState{
		{SubjectID: "subj-hard", LifecycleState: LifecycleStateUnresolved},
	})
	r.RecordCognition(startAt)

	// T+60s: Pulse spontaneous wake → cognition succeeds.
	clock.Set(startAt.Add(60 * time.Second))
	r.evaluate()
	if !r.OpportunitySnapshot().Opportunity {
		t.Fatal("expected opportunity at T+60s (first wake)")
	}
	if mind.entered != 1 {
		t.Fatalf("expected 1 mind entry at T+60s, got %d", mind.entered)
	}
	enteredBefore := mind.entered

	// T+65s: within MinWakeSpacing (120s). Pulse evaluate produces no opportunity.
	clock.Set(startAt.Add(65 * time.Second))
	r.evaluate()
	oppPulse := r.OpportunitySnapshot()
	if oppPulse.Opportunity {
		t.Fatal("expected MinWakeSpacing to block Pulse spontaneous wake at T+65s")
	}

	// However, an external caller can still call EnterPulseWake directly.
	// This simulates a "hard admitted obligation" — something external that
	// bypasses Pulse's evaluation entirely.
	hardWake := PulseWake{
		AdmittedAt:        startAt.Add(65 * time.Second),
		Pressure:          1.0,
		EffectivePressure: 1.0,
		ActivationSignals: []float64{1.0},
		Inhibition:        InhibitionBreakdown{},
		Subjects:          []SubjectActivation{{SubjectID: "subj-hard", Neglect: 1.0}},
		RandomSample:      0.01,
	}
	err := r.mindEntry.EnterPulseWake(context.Background(), hardWake)
	if err != nil {
		t.Fatalf("hard obligation EnterPulseWake failed: %v", err)
	}
	if mind.entered != enteredBefore+1 {
		t.Fatalf("expected mind entry for hard obligation, got entered=%d (was %d)",
			mind.entered, enteredBefore)
	}

	t.Log("M5 HardObligationsNotBlocked: verified")
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// assertSubjectSettled checks that a subject's LastPresentedAt and
// LastSettledAt are both set to the expected time (within tolerance).
func assertSubjectSettled(t *testing.T, r *Runner, id string, expected time.Time) {
	t.Helper()
	subjs := r.SubjectSnapshots()
	for _, subj := range subjs {
		if subj.SubjectID == id {
			if subj.LastPresentedAt.IsZero() || !subj.LastPresentedAt.Equal(expected) {
				t.Fatalf("subject %s: expected LastPresentedAt=%v, got %v",
					id, expected, subj.LastPresentedAt)
			}
			if subj.LastSettledAt.IsZero() || !subj.LastSettledAt.Equal(expected) {
				t.Fatalf("subject %s: expected LastSettledAt=%v, got %v",
					id, expected, subj.LastSettledAt)
			}
			return
		}
	}
	t.Fatalf("subject %s not found", id)
}
