package pulse

import (
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
)

// EvaluateOpportunity is a pure function that evaluates a stochastic
// spontaneous-wake opportunity. It takes the current time, a SignalSnapshot
// (from EvaluateSignals), the runner's PulseSnapshot, configuration,
// whether a cognition run is currently executing, and an RNG source.
//
// Pipeline:
//  1. Compute raw pressure via saturating union of all activation signals.
//  2. Apply soft inhibition (cooldown, budget) → effective pressure.
//  3. Evaluate hard optional-wake guards (min wake spacing, cognition-run occupancy).
//  4. If ineligible (guard blocked or effective pressure = 0) → no RNG draw,
//     opportunity=false.
//  5. If eligible → exactly one RNG draw, opportunity = sample < effective_pressure.
//  6. Pressure 1 always succeeds (sample < 1 is always true for [0,1) uniform).
//
// EvaluateOpportunity has no side effects: no Mind invocation, no inference,
// no wake callback, no LastSpontaneousWakeAt mutation, no presentation/settling
// mutation, no autonomy toggle/policy.
func EvaluateOpportunity(now time.Time, sig SignalSnapshot, pulseState PulseSnapshot, cfg config.PulseConfig, cognitionRunActive bool, rng RNG) OpportunitySnapshot {
	// Step 1-2: Pressure and soft inhibition.
	pressure, effectivePressure, activationSignals, subjectIDs := EvaluatePressure(sig)

	// Step 3: Hard optional-wake guards.
	guards := EvaluateGuards(now, sig, pulseState, cfg, cognitionRunActive)

	// Step 4: Eligibility.
	eligible := effectivePressure > 0
	for _, g := range guards {
		if g.Blocked {
			eligible = false
			break
		}
	}

	// Step 5-6: Stochastic opportunity.
	var sample *float64
	opportunity := false

	if eligible && rng != nil {
		s := rng.Float64()
		sample = &s
		opportunity = s < effectivePressure
	}

	return OpportunitySnapshot{
		EvaluatedAt:       now,
		Type:              "spontaneous",
		Pressure:          pressure,
		EffectivePressure: effectivePressure,
		ActivationSignals: activationSignals,
		Inhibition: InhibitionBreakdown{
			Cooldown: sig.Cooldown,
			Budget:   sig.Budget,
		},
		Subjects:     subjectIDs,
		Eligible:     eligible,
		Guards:       guards,
		RandomSample: sample,
		Opportunity:  opportunity,
	}
}

// EvaluateGuards runs all hard guards and returns their results.
//
// Guards:
//   - MinWakeSpacing: if cfg.MinWakeSpacing > 0 and a previous spontaneous wake
//     exists and the elapsed time since it is less than the spacing, the guard
//     blocks. Zero spacing = disabled. No previous wake = does not block.
//   - CognitionRun: if cognitionRunActive is true, the guard blocks. This is
//     runtime concurrency protection, not autonomy control.
func EvaluateGuards(now time.Time, sig SignalSnapshot, pulseState PulseSnapshot, cfg config.PulseConfig, cognitionRunActive bool) []GuardResult {
	guards := make([]GuardResult, 0, 2)

	// --- MinWakeSpacing guard ---
	minWakeSpacing := cfg.MinWakeSpacing
	if minWakeSpacing > 0 && !pulseState.LastSpontaneousWakeAt.IsZero() {
		elapsed := now.Sub(pulseState.LastSpontaneousWakeAt).Seconds()
		blocked := elapsed < float64(minWakeSpacing)
		msg := fmt.Sprintf("elapsed=%.0fs spacing=%ds", elapsed, minWakeSpacing)
		if blocked {
			msg = fmt.Sprintf("elapsed=%.0fs < spacing=%ds — blocked", elapsed, minWakeSpacing)
		} else {
			msg = fmt.Sprintf("elapsed=%.0fs >= spacing=%ds — allowed", elapsed, minWakeSpacing)
		}
		guards = append(guards, GuardResult{
			Reason:  GuardReasonMinWakeSpacing,
			Blocked: blocked,
			Message: msg,
		})
	} else if minWakeSpacing > 0 && pulseState.LastSpontaneousWakeAt.IsZero() {
		// No previous wake → spacing does not block.
		guards = append(guards, GuardResult{
			Reason:  GuardReasonMinWakeSpacing,
			Blocked: false,
			Message: "no previous spontaneous wake — spacing does not block",
		})
	}
	// If minWakeSpacing == 0, the guard is absent from the list (disabled).

	// --- CognitionRun guard ---
	if cognitionRunActive {
		guards = append(guards, GuardResult{
			Reason:  GuardReasonCognitionRun,
			Blocked: true,
			Message: "cognition run is already executing — spontaneous opportunity blocked",
		})
	} else {
		guards = append(guards, GuardResult{
			Reason:  GuardReasonCognitionRun,
			Blocked: false,
			Message: "no cognition run in progress — allowed",
		})
	}

	return guards
}
