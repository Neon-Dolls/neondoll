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
// It returns the flattened list of signals and the list of subject IDs that
// contributed non-zero subject-level signals.
//
// The global idle signal is always included (even when zero, it contributes
// nothing to the union). Subjects with all-zero signals are excluded from
// both the signal list and the returned ID list.
func activationSignalsFromSnapshot(snap SignalSnapshot) (signals []float64, contributingSubjectIDs []string) {
	// Idle is always present; include only if non-zero.
	if snap.Idle > 0 {
		signals = append(signals, snap.Idle)
	}

	// Collect subject IDs only for subjects with at least one non-zero signal.
	for _, subj := range snap.Subjects {
		// Determine the maximum signal value for this subject.
		maxSig := math.Max(math.Max(subj.Neglect, subj.Change), subj.Unfinished)
		// Clamp to [0, 1] in case of float artifacts.
		if maxSig > 1 {
			maxSig = 1
		}
		if maxSig > 0 {
			signals = append(signals, maxSig)
			contributingSubjectIDs = append(contributingSubjectIDs, subj.SubjectID)
		}
	}

	return signals, contributingSubjectIDs
}

// EvaluatePressure is a pure function that computes raw pressure (saturating
// union), effective pressure (after soft inhibition), and returns the
// contributing signal values and subject IDs.
//
// It does not consume RNG, mutate state, or perform hard-guard checks.
func EvaluatePressure(snap SignalSnapshot) (pressure, effectivePressure float64, activationSignals []float64, subjects []string) {
	signals, subjectIDs := activationSignalsFromSnapshot(snap)
	activationSignals = signals
	subjects = subjectIDs
	pressure = CombineActivation(signals)
	effectivePressure = ApplyInhibition(pressure, snap.Cooldown, snap.Budget)
	return
}
