package pulse

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
)

// --- Pressure tests ---

func TestCombineActivation_ZeroActivation(t *testing.T) {
	p := CombineActivation(nil)
	if p != 0 {
		t.Fatalf("CombineActivation(nil) = %f, want 0", p)
	}
	p = CombineActivation([]float64{})
	if p != 0 {
		t.Fatalf("CombineActivation([]) = %f, want 0", p)
	}
	p = CombineActivation([]float64{0, 0, 0})
	if p != 0 {
		t.Fatalf("CombineActivation([0,0,0]) = %f, want 0", p)
	}
}

func TestCombineActivation_IdleOnlyPressure(t *testing.T) {
	p := CombineActivation([]float64{0.5})
	if p != 0.5 {
		t.Fatalf("CombineActivation([0.5]) = %f, want 0.5", p)
	}
}

func TestCombineActivation_SaturatingUnion_UnderSum(t *testing.T) {
	// Each 0.3 → combined = 1 - (0.7)^3 ≈ 0.657
	p := CombineActivation([]float64{0.3, 0.3, 0.3})
	want := 1.0 - math.Pow(0.7, 3)
	if math.Abs(p-want) > 1e-9 {
		t.Fatalf("CombineActivation([0.3, 0.3, 0.3]) = %f, want %f", p, want)
	}
	// Verify it's less than sum (0.9)
	if p >= 0.9 {
		t.Fatalf("saturating union should be < sum, got %f >= 0.9", p)
	}
}

func TestCombineActivation_MultipleSubjects(t *testing.T) {
	p := CombineActivation([]float64{0.4, 0.3, 0.2, 0.1})
	want := 1.0 - (1-0.4)*(1-0.3)*(1-0.2)*(1-0.1)
	if math.Abs(p-want) > 1e-9 {
		t.Fatalf("CombineActivation([0.4,0.3,0.2,0.1]) = %f, want %f", p, want)
	}
}

func TestCombineActivation_SubjectOrderStability(t *testing.T) {
	signals1 := []float64{0.1, 0.2, 0.3, 0.4, 0.5}
	signals2 := []float64{0.5, 0.4, 0.3, 0.2, 0.1}
	p1 := CombineActivation(signals1)
	p2 := CombineActivation(signals2)
	if p1 != p2 {
		t.Fatalf("order should not matter: %f != %f", p1, p2)
	}
}

// --- Inhibition tests ---

func TestApplyInhibition_PartialCooldown(t *testing.T) {
	eff := ApplyInhibition(0.8, 0.3, 0)
	want := 0.8 * (1 - 0.3) * (1 - 0)
	if math.Abs(eff-want) > 1e-9 {
		t.Fatalf("ApplyInhibition(0.8, 0.3, 0) = %f, want %f", eff, want)
	}
}

func TestApplyInhibition_PartialBudget(t *testing.T) {
	eff := ApplyInhibition(0.8, 0, 0.4)
	want := 0.8 * (1 - 0) * (1 - 0.4)
	if math.Abs(eff-want) > 1e-9 {
		t.Fatalf("ApplyInhibition(0.8, 0, 0.4) = %f, want %f", eff, want)
	}
}

func TestApplyInhibition_CombinedInhibition(t *testing.T) {
	eff := ApplyInhibition(0.9, 0.3, 0.2)
	want := 0.9 * (1 - 0.3) * (1 - 0.2)
	if math.Abs(eff-want) > 1e-9 {
		t.Fatalf("ApplyInhibition(0.9, 0.3, 0.2) = %f, want %f", eff, want)
	}
}

func TestApplyInhibition_FullInhibition(t *testing.T) {
	if eff := ApplyInhibition(0.9, 1.0, 0); eff != 0 {
		t.Fatalf("ApplyInhibition(0.9, 1, 0) = %f, want 0", eff)
	}
	if eff := ApplyInhibition(0.9, 0, 1.0); eff != 0 {
		t.Fatalf("ApplyInhibition(0.9, 0, 1) = %f, want 0", eff)
	}
}

func TestApplyInhibition_ClampSafety(t *testing.T) {
	eff := ApplyInhibition(0.5, math.NaN(), 0)
	if math.IsNaN(eff) {
		t.Fatal("ApplyInhibition should not propagate NaN")
	}
	eff = ApplyInhibition(0.5, 0, -1)
	if eff != 0.5 {
		t.Fatalf("ApplyInhibition(0.5, 0, -1) = %f, want 0.5", eff)
	}
}

// --- Guard tests ---

func TestEvaluateGuards_SpacingDisabled(t *testing.T) {
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: time.Now(), Cooldown: 0, Budget: 0}
	guards := EvaluateGuards(time.Now(), sig, PulseSnapshot{}, cfg, false)
	// Spacing guard absent when MinWakeSpacing=0, so only cognition guard
	foundCog := false
	for _, g := range guards {
		if g.Reason == GuardReasonCognitionRun && g.Blocked {
			t.Fatal("cognition guard should not be blocked")
		}
	}
	_ = foundCog
}

func TestEvaluateGuards_NoPreviousSpontaneousWake(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 300}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: time.Time{},
	}
	guards := EvaluateGuards(now, sig, ps, cfg, false)
	hasSpacing := false
	for _, g := range guards {
		if g.Reason == GuardReasonMinWakeSpacing && g.Blocked {
			t.Fatal("spacing should not block without previous wake")
		}
		if g.Reason == GuardReasonMinWakeSpacing {
			hasSpacing = true
		}
	}
	if !hasSpacing {
		t.Fatal("expected spacing guard")
	}
}

func TestEvaluateGuards_SpacingJustBelow(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 300}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: now.Add(-299 * time.Second),
	}
	guards := EvaluateGuards(now, sig, ps, cfg, false)
	for _, g := range guards {
		if g.Reason == GuardReasonMinWakeSpacing && !g.Blocked {
			t.Fatalf("spacing should block: %s", g.Message)
		}
	}
}

func TestEvaluateGuards_SpacingExactlyAt(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 300}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastSpontaneousWakeAt: now.Add(-300 * time.Second),
	}
	guards := EvaluateGuards(now, sig, ps, cfg, false)
	for _, g := range guards {
		if g.Reason == GuardReasonMinWakeSpacing && g.Blocked {
			t.Fatalf("spacing should allow at boundary: %s", g.Message)
		}
	}
}

func TestEvaluateGuards_SpacingBeyond(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 300}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastSpontaneousWakeAt: now.Add(-600 * time.Second),
	}
	guards := EvaluateGuards(now, sig, ps, cfg, false)
	for _, g := range guards {
		if g.Reason == GuardReasonMinWakeSpacing && g.Blocked {
			t.Fatalf("spacing should allow beyond boundary: %s", g.Message)
		}
	}
}

func TestEvaluateGuards_CognitionRunActive(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 0}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: time.Time{},
	}
	guards := EvaluateGuards(now, sig, ps, cfg, true)
	foundCog := false
	for _, g := range guards {
		if g.Reason == GuardReasonCognitionRun {
			foundCog = true
			if !g.Blocked {
				t.Fatal("cognition guard should block when active")
			}
		}
	}
	if !foundCog {
		t.Fatal("expected cognition guard")
	}
}

func TestEvaluateGuards_MultipleSimultaneous(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 600}
	sig := SignalSnapshot{At: now, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: now.Add(-60 * time.Second),
	}
	guards := EvaluateGuards(now, sig, ps, cfg, true)
	for _, g := range guards {
		if !g.Blocked {
			t.Fatalf("guard %s should block", g.Reason)
		}
	}
}

// --- Opportunity tests ---

func TestEvaluateOpportunity_ZeroActivation(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.5}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if opp.Pressure != 0 {
		t.Fatalf("expected pressure 0, got %f", opp.Pressure)
	}
	if opp.Opportunity {
		t.Fatal("expected no opportunity with zero activation")
	}
}

func TestEvaluateOpportunity_IdleOnlySuccess(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.8, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.5}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if !opp.Opportunity {
		t.Fatal("expected opportunity=true")
	}
	if opp.RandomSample == nil {
		t.Fatal("expected non-nil RandomSample")
	}
	if *opp.RandomSample != 0.5 {
		t.Fatalf("expected sample 0.5, got %f", *opp.RandomSample)
	}
	if rng.index != 1 {
		t.Fatalf("expected 1 RNG draw, got index=%d", rng.index)
	}
}

func TestEvaluateOpportunity_IdleOnlyFailure(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.3}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.5}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if opp.Opportunity {
		t.Fatal("expected opportunity=false")
	}
}

func TestEvaluateOpportunity_Pressure1AlwaysSucceeds(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 1.0, Cooldown: 0, Budget: 0}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.999}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if !opp.Opportunity {
		t.Fatal("pressure 1 must always succeed")
	}
}

func TestEvaluateOpportunity_ZeroRNGDrawsWhenIneligible(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 3600}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: now.Add(-1 * time.Second),
	}
	sig := SignalSnapshot{At: now, Idle: 0.8}
	rng := &fakeRNG{samples: []float64{0.5}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if opp.Opportunity {
		t.Fatal("expected no opportunity when spacing blocks")
	}
	if rng.index != 0 {
		t.Fatalf("expected 0 RNG draws when ineligible, got %d", rng.index)
	}
}

func TestEvaluateOpportunity_ExactlyOneDrawWhenEligible(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	sig := SignalSnapshot{At: now, Idle: 0.5}
	rng := &fakeRNG{samples: []float64{0.3, 0.7, 0.9}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if !opp.Opportunity {
		t.Fatal("expected opportunity=true")
	}
	if rng.index != 1 {
		t.Fatalf("expected exactly 1 RNG draw, got %d", rng.index)
	}
}

func TestEvaluateOpportunity_DeterministicSameState(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.7}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}

	opp1 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.3}})
	opp2 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.3}})
	if opp1.Pressure != opp2.Pressure {
		t.Fatal("deterministic: pressure mismatch")
	}
	if opp1.EffectivePressure != opp2.EffectivePressure {
		t.Fatal("deterministic: effective pressure mismatch")
	}
	if opp1.Eligible != opp2.Eligible {
		t.Fatal("deterministic: eligible mismatch")
	}
	if opp1.Opportunity != opp2.Opportunity {
		t.Fatal("deterministic: opportunity mismatch")
	}
	if *opp1.RandomSample != *opp2.RandomSample {
		t.Fatal("deterministic: sample mismatch")
	}
}

func TestEvaluateOpportunity_DeterministicFailure(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.2}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.5}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if opp.Opportunity {
		t.Fatal("expected failure with idle=0.2, sample=0.5")
	}
	if rng.index != 1 {
		t.Fatalf("expected 1 RNG draw, got %d", rng.index)
	}
}

func TestEvaluateOpportunity_DifferentRNGDifferentOutcome(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.5}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}

	opp1 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.3}})
	opp2 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.7}})

	if opp1.Pressure != opp2.Pressure {
		t.Fatal("pressure should be identical before sample")
	}
	if opp1.Opportunity == opp2.Opportunity {
		t.Fatal("expected different outcomes with different RNG samples")
	}
}

func TestEvaluateOpportunity_IdleOnlyEmptySubjects(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 0.6}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	rng := &fakeRNG{samples: []float64{0.3}}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
	if len(opp.Subjects) != 0 {
		t.Fatalf("expected empty subjects for idle-only, got %v", opp.Subjects)
	}
	if !opp.Opportunity {
		t.Fatal("expected opportunity")
	}
}

func TestEvaluateOpportunity_ContributingSubjects(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{
		At:   now,
		Idle: 0.2,
		Subjects: []SubjectSignal{
			{SubjectID: "subject-1", Neglect: 0.5, Change: 0.3, Unfinished: 0.2},
			{SubjectID: "subject-2", Neglect: 0.1, Change: 0.1, Unfinished: 0.1},
			{SubjectID: "subject-3", Neglect: 0, Change: 0, Unfinished: 0},
		},
		Cooldown: 0,
		Budget:   0,
	}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.01}})
	// subject-1 has max signal 0.5, subject-2 has max 0.1, subject-3 has max 0
	if len(opp.Subjects) != 2 {
		t.Fatalf("expected 2 contributing subjects, got %v", opp.Subjects)
	}
	foundS1, foundS2 := false, false
	for _, s := range opp.Subjects {
		switch s {
		case "subject-1":
			foundS1 = true
		case "subject-2":
			foundS2 = true
		}
	}
	if !foundS1 || !foundS2 {
		t.Fatalf("expected subject-1 and subject-2, got %v", opp.Subjects)
	}
}

func TestEvaluateOpportunity_PartialCooldown(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 0}
	sig := SignalSnapshot{At: now, Idle: 0.8, Cooldown: 0.3, Budget: 0}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.1}})
	wantEff := 0.8 * (1 - 0.3) * (1 - 0)
	if math.Abs(opp.EffectivePressure-wantEff) > 1e-9 {
		t.Fatalf("expected effective pressure %v, got %v", wantEff, opp.EffectivePressure)
	}
	if opp.Inhibition.Cooldown != 0.3 {
		t.Fatalf("expected cooldown 0.3, got %f", opp.Inhibition.Cooldown)
	}
}

func TestEvaluateOpportunity_PartialBudget(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 0}
	sig := SignalSnapshot{At: now, Idle: 0.8, Cooldown: 0, Budget: 0.4}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.1}})
	wantEff := 0.8 * (1 - 0) * (1 - 0.4)
	if math.Abs(opp.EffectivePressure-wantEff) > 1e-9 {
		t.Fatalf("expected effective pressure %v, got %v", wantEff, opp.EffectivePressure)
	}
	if opp.Inhibition.Budget != 0.4 {
		t.Fatalf("expected budget 0.4, got %f", opp.Inhibition.Budget)
	}
}

func TestEvaluateOpportunity_CombinedInhibition(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 0}
	sig := SignalSnapshot{At: now, Idle: 1.0, Cooldown: 0.3, Budget: 0.2}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.1}})
	wantEff := 1.0 * (1 - 0.3) * (1 - 0.2)
	if math.Abs(opp.EffectivePressure-wantEff) > 1e-9 {
		t.Fatalf("expected effective pressure %v, got %v", wantEff, opp.EffectivePressure)
	}
}

func TestEvaluateOpportunity_FullInhibition(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{At: now, Idle: 1.0, Cooldown: 1.0, Budget: 0}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.1}})
	if opp.EffectivePressure != 0 {
		t.Fatal("full cooldown should zero effective pressure")
	}
	if opp.Eligible {
		t.Fatal("should not be eligible with zero effective pressure")
	}
	if opp.RandomSample != nil {
		t.Fatal("no RNG draws expected with effective pressure=0")
	}
}

func TestEvaluateOpportunity_SpacingGuardBlocks(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, MinWakeSpacing: 300}
	ps := PulseSnapshot{
		LastCognitionAt:       now.Add(-10 * time.Minute),
		LastSpontaneousWakeAt: now.Add(-60 * time.Second),
	}
	sig := SignalSnapshot{At: now, Idle: 0.9}
	opp := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.5}})
	if opp.Eligible {
		t.Fatal("should be ineligible due to spacing guard")
	}
	if opp.Opportunity {
		t.Fatal("should not have opportunity when spacing blocks")
	}
}

// --- Runner integration tests ---

func TestRunner_OpportunitySnapshot(t *testing.T) {
	ref := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    100,
		MinWakeSpacing: 0,
	}
	rng := &fakeRNG{samples: []float64{0.3}}

	r, _, tickCh, ackCh := newTestRunner(t, cfg, ref)
	r.rng = rng

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Establish cognition baseline so idle signal is non-zero.
	r.RecordCognition(ref.Add(-50 * time.Second))

	// Advance clock and inject tick.
	r.clock.(*fakeClock).Advance(2 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap := r.OpportunitySnapshot()
	if snap.EvaluatedAt.IsZero() {
		t.Fatal("expected non-zero EvaluatedAt")
	}
	if snap.Pressure <= 0 {
		t.Fatal("expected positive pressure after tick")
	}
}

func TestRunner_OpportunityBackwardsTime(t *testing.T) {
	ref := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, IdleHorizon: 100, MinWakeSpacing: 0}

	r, _, tickCh, ackCh := newTestRunner(t, cfg, ref)
	rng := &fakeRNG{samples: []float64{0.3}}
	r.rng = rng

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Establish cognition baseline so idle signal is non-zero.
	r.RecordCognition(ref.Add(-50 * time.Second))

	// Forward tick
	r.clock.(*fakeClock).Advance(10 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap1 := r.OpportunitySnapshot()

	// Backwards tick
	r.clock.(*fakeClock).Set(ref.Add(5 * time.Second))
	triggerTick(t, tickCh, ackCh)

	snap2 := r.OpportunitySnapshot()

	if snap1.EvaluatedAt.IsZero() {
		t.Fatal("first snapshot should have non-zero time")
	}
	if snap2.EvaluatedAt.IsZero() {
		t.Fatal("backwards-time snapshot should have non-zero time")
	}
}

func TestRunner_RaceSafeSnapshots(t *testing.T) {
	ref := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, IdleHorizon: 100, MinWakeSpacing: 0}

	r, fc, _, _ := newTestRunner(t, cfg, ref)
	rng := &fakeRNG{samples: []float64{0.3, 0.5, 0.7, 0.9, 0.2, 0.4, 0.6, 0.8}}
	r.rng = rng

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			fc.Advance(time.Second)
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()

	for i := 0; i < 50; i++ {
		sig := r.SignalSnapshot()
		opp := r.OpportunitySnapshot()
		_ = sig
		_ = opp
		time.Sleep(time.Millisecond)
	}
	<-done
}

// --- Backwards-time preservation ---

func TestBackwardsTime_PreservesSignalSnapshot(t *testing.T) {
	ref := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, IdleHorizon: 100}
	r, _, tickCh, ackCh := newTestRunner(t, cfg, ref)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Forward
	r.clock.(*fakeClock).Advance(10 * time.Second)
	triggerTick(t, tickCh, ackCh)
	fwdSnap := r.SignalSnapshot()

	// Backwards
	r.clock.(*fakeClock).Set(ref.Add(5 * time.Second))
	triggerTick(t, tickCh, ackCh)
	bwdSnap := r.SignalSnapshot()

	if fwdSnap.At.IsZero() {
		t.Fatal("forward snapshot should not be zero")
	}
	if bwdSnap.At.IsZero() {
		t.Fatal("backwards snapshot should not be zero")
	}
}

func TestBackwardsTime_PreservesOpportunitySnapshot(t *testing.T) {
	ref := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true, IdleHorizon: 100, MinWakeSpacing: 0}
	r, _, tickCh, ackCh := newTestRunner(t, cfg, ref)
	r.rng = &fakeRNG{samples: []float64{0.5}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Forward
	r.clock.(*fakeClock).Advance(10 * time.Second)
	triggerTick(t, tickCh, ackCh)
	fwdOpp := r.OpportunitySnapshot()

	// Backwards
	r.clock.(*fakeClock).Set(ref.Add(5 * time.Second))
	triggerTick(t, tickCh, ackCh)
	bwdOpp := r.OpportunitySnapshot()

	if fwdOpp.EvaluatedAt.IsZero() {
		t.Fatal("forward opp snapshot should not be zero")
	}
	if bwdOpp.EvaluatedAt.IsZero() {
		t.Fatal("backwards opp snapshot should not be zero")
	}
}

// --- Extended reproducibility ---

func TestExtendedReproducibility(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cfg := config.PulseConfig{Enabled: true}
	sig := SignalSnapshot{
		At:       now,
		Idle:     0.7,
		Cooldown: 0.1,
		Budget:   0.05,
	}
	ps := PulseSnapshot{LastCognitionAt: now.Add(-10 * time.Minute)}

	opp1 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.42}})
	opp2 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.42}})
	opp3 := EvaluateOpportunity(now, sig, ps, cfg, false, &fakeRNG{samples: []float64{0.99}})

	if opp1.Pressure != opp2.Pressure || opp2.Pressure != opp3.Pressure {
		t.Fatal("same state → same pressure")
	}
	if opp1.EffectivePressure != opp2.EffectivePressure || opp2.EffectivePressure != opp3.EffectivePressure {
		t.Fatal("same state → same effective pressure")
	}
	if opp1.Opportunity != opp2.Opportunity {
		t.Fatal("same state + same RNG → same opportunity")
	}
	if *opp1.RandomSample != *opp2.RandomSample {
		t.Fatal("same state + same RNG → same sample")
	}
	if *opp1.RandomSample == *opp3.RandomSample {
		t.Fatal("different RNG → different sample expected")
	}
}

// --- Conformance proof: idle-only spontaneous opportunity ---

func TestIdleOnlyConformanceProof(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
		cfg := config.PulseConfig{
			Enabled:        true,
			IdleHorizon:    3600,
			MinWakeSpacing: 0,
		}
		ps := PulseSnapshot{LastCognitionAt: now.Add(-30 * time.Minute)}
		sig := SignalSnapshot{At: now, Idle: 0.5}
		rng := &fakeRNG{samples: []float64{0.3}}
		opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
		if !opp.Opportunity {
			t.Fatal("idle-only conformance: expected opportunity=true")
		}
		if len(opp.Subjects) != 0 {
			t.Fatalf("idle-only conformance: expected empty subjects, got %v", opp.Subjects)
		}
	})

	t.Run("failure", func(t *testing.T) {
		now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
		cfg := config.PulseConfig{
			Enabled:        true,
			IdleHorizon:    3600,
			MinWakeSpacing: 0,
		}
		ps := PulseSnapshot{LastCognitionAt: now.Add(-30 * time.Minute)}
		sig := SignalSnapshot{At: now, Idle: 0.5}
		rng := &fakeRNG{samples: []float64{0.7}}
		opp := EvaluateOpportunity(now, sig, ps, cfg, false, rng)
		if opp.Opportunity {
			t.Fatal("idle-only conformance (failure): expected opportunity=false")
		}
		if len(opp.Subjects) != 0 {
			t.Fatalf("expected empty subjects, got %v", opp.Subjects)
		}
	})
}
