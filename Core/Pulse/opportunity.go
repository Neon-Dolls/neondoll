package pulse

import (
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
)

// EvaluateGuards checks all hard guards and returns whether the evaluation
// is eligible (no guard blocked). It does NOT mutate state.
//
// Guards checked:
//   - MinWakeSpacing: time since LastSpontaneousWakeAt < MinWakeSpacing seconds
//   - CognitionRunActive: if another cognition run is executing
func EvaluateGuards(now time.Time, ps PulseSnapshot, cfg config.PulseConfig, cognitionRunActive bool) (eligible bool, guards []GuardResult) {
	var blocked bool

	// MinWakeSpacing guard
	spacingResult := GuardResult{Reason: GuardReasonMinWakeSpacing}
	if cfg.MinWakeSpacing > 0 && !ps.LastSpontaneousWakeAt.IsZero() {
		elapsed := now.Sub(ps.LastSpontaneousWakeAt)
		if elapsed.Seconds() < float64(cfg.MinWakeSpacing) {
			spacingResult.Blocked = true
			spacingResult.Message = "min wake spacing not met"
			blocked = true
		}
	}
	if !spacingResult.Blocked {
		spacingResult.Message = "spacing ok"
	}
	guards = append(guards, spacingResult)

	// CognitionRunActive guard
	cognitionGuard := GuardResult{
		Reason:  GuardReasonCognitionRun,
		Blocked: cognitionRunActive,
	}
	if cognitionRunActive {
		cognitionGuard.Message = "cognition run already active"
		blocked = true
	} else {
		cognitionGuard.Message = "no active cognition run"
	}
	guards = append(guards, cognitionGuard)

	return !blocked, guards
}

// EvaluateOpportunity is a pure function that performs one complete
// stochastic opportunity evaluation. It does NOT mutate any state; the caller
// is responsible for persisting results.
//
// Returns an OpportunitySnapshot describing the full evaluation.
func EvaluateOpportunity(now time.Time, snap SignalSnapshot, ps PulseSnapshot, cfg config.PulseConfig, cognitionRunActive bool, rng RNG) OpportunitySnapshot {
	// 1. Hard guards
	eligible, guards := EvaluateGuards(now, ps, cfg, cognitionRunActive)

	// 2. Pressure computation
	pressure, effectivePressure, signals, subjects := EvaluatePressure(snap)

	// Eligible means guards passed AND effectivePressure > 0.
	// This prevents RNG draws when the pressure is zeroed by inhibition.
	eligible = eligible && effectivePressure > 0

	// 3. Stochastic draw
	var sample *float64
	opportunity := false

	if eligible && effectivePressure > 0 {
		if rng == nil {
			panic("pulse: EvaluateOpportunity: eligible evaluation with effectivePressure>0 but rng is nil — " +
				"production must provide a valid RNG source. Tests should inject fakeRNG.")
		}
		s := rng.Float64()
		sample = &s
		opportunity = s < effectivePressure
	}

	return OpportunitySnapshot{
		EvaluatedAt:       now,
		Type:              "spontaneous",
		Pressure:          pressure,
		EffectivePressure: effectivePressure,
		ActivationSignals: signals,
		Inhibition: InhibitionBreakdown{
			Cooldown: snap.Cooldown,
			Budget:   snap.Budget,
		},
		Subjects:     subjects,
		Eligible:     eligible,
		Guards:       guards,
		RandomSample: sample,
		Opportunity:  opportunity,
	}
}
