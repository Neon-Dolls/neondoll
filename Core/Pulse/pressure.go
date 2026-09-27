package pulse

import "math"

// CombineActivation computes the saturating-union pressure from a set of
// activation signal values. The formula is the De Morgan dual of the product
// of complementary probabilities:
//
//	pressure = 1 - ∏(1 - signal_i)
//
// This ensures:
//   - Bounded: result is in [0, 1].
//   - Saturating union: any single input at 1.0 saturates the result at 1.0.
//   - Diminishing returns: multiple weak signals compound but never exceed
//     their individual sum (sub-additive).
//   - Order-independent: the result depends only on the multiset of signals.
//   - Zero-input returns 0: ∏ over an empty set is 1, so 1 - 1 = 0.
//
// NaN and ±Inf inputs are treated as 0.
func CombineActivation(signals []float64) float64 {
	if len(signals) == 0 {
		return 0
	}
	product := 1.0
	for _, s := range signals {
		if math.IsNaN(s) || math.IsInf(s, 0) {
			s = 0
		}
		if s < 0 {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		product *= 1 - s
	}
	return 1 - product
}

// ApplyInhibition applies soft inhibition factors to a raw pressure value
// and returns the effective (shaped) pressure.
//
//	effective = pressure * (1 - cooldown) * (1 - budget)
//
// Both cooldown and budget are clamped to [0, 1] before computation.
// When pressure is 0, the result is always 0 regardless of inhibition.
// When inhibition factors are 0, they are no-ops (multiply by 1).
func ApplyInhibition(pressure, cooldown, budget float64) float64 {
	if math.IsNaN(pressure) || math.IsInf(pressure, 0) {
		return 0
	}
	cd := clampInhibition(cooldown)
	bg := clampInhibition(budget)
	return pressure * (1 - cd) * (1 - bg)
}

// activationSignalsFromSnapshot extracts all non-zero activation signal values
// from a SignalSnapshot for the purpose of computing saturating-union pressure.
// It returns the flattened list of signals and structured activation evidence.
//
// Unlike M2 which collapsed each subject's neglect, change, and unfinished
// into a single max per subject, M3 adds EVERY non-zero signal individually
// into the saturating union. This means multiple activation signals from the
// SAME subject all contribute independently — e.g. neglect=0.5 AND change=0.5
// on the same subject contributes 1 - ((1-0.5)*(1-0.5)) = 0.75, not 0.5.
//
// The global idle signal is always included (even when zero, it contributes
// nothing to the union). Subjects with all-zero signals are excluded from
// both the signal list and the activation evidence.
func activationSignalsFromSnapshot(snap SignalSnapshot) (signals []float64, activations []SubjectActivation) {
	// Idle is always included as a signal (even when 0, it provides traceability).
	// It is NOT wrapped in a SubjectActivation — idle is a global activation signal,
	// not a subject-level one.
	if snap.Idle > 0 {
		signals = append(signals, clampSignal(snap.Idle))
	}

	// Collect every non-zero activation signal from each subject individually.
	// Do NOT collapse a subject's signals to a single max — each signal
	// contributes independently to the saturating union.
	for _, subj := range snap.Subjects {
		act := SubjectActivation{SubjectID: subj.SubjectID}
		hasNonZero := false

		if subj.Neglect > 0 {
			v := clampSignal(subj.Neglect)
			signals = append(signals, v)
			act.Neglect = v
			hasNonZero = true
		}
		if subj.Change > 0 {
			v := clampSignal(subj.Change)
			signals = append(signals, v)
			act.Change = v
			hasNonZero = true
		}
		if subj.Unfinished > 0 {
			v := clampSignal(subj.Unfinished)
			signals = append(signals, v)
			act.Unfinished = v
			hasNonZero = true
		}

		if hasNonZero {
			activations = append(activations, act)
		}
	}

	return signals, activations
}

// clampSignal clamps a signal value to [0, 1], treating NaN/Inf as 0.
func clampSignal(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// EvaluatePressure is a pure function that computes raw pressure (saturating
// union), effective pressure (after soft inhibition), and returns the
// contributing signal values and subject activation evidence.
//
// It does not consume RNG, mutate state, or perform hard-guard checks.
func EvaluatePressure(snap SignalSnapshot) (pressure, effectivePressure float64, activationSignals []float64, subjects []SubjectActivation) {
	signals, activations := activationSignalsFromSnapshot(snap)
	activationSignals = signals
	subjects = activations
	pressure = CombineActivation(signals)
	effectivePressure = ApplyInhibition(pressure, snap.Cooldown, snap.Budget)
	return
}
