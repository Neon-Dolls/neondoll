package pulse

import (
	"math"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
)

// secsAsDuration converts an int64 seconds value to time.Duration.
func secsAsDuration(s int64) time.Duration {
	return time.Duration(s) * time.Second
}

// clampInhibition clamps a float64 to [0, 1] for inhibition signals.
func clampInhibition(v float64) float64 {
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

// EvaluateSignals is a pure function that evaluates all activation and
// inhibition signals for a single Pulse tick. It takes an explicit time
// (now), Pulse configuration, the current PulseSnapshot (bookkeeping),
// a list of subject states (supplied by Core), and inhibition inputs
// (also from Core). It returns a complete SignalSnapshot.
//
// EvaluateSignals has no side effects: no RNG consumption, no mutation of
// Core state, no inference, no semantic inspection, no waking of Doll Mind.
// It is deterministic given the same inputs.
//
// Signal formulas (per Core 3 M2 contract):
//
//	Activation:
//	  idle      = normalize(elapsed_since_last_cognition, idle_horizon)
//	  neglect   = normalize(elapsed_since_last_present,  neglect_horizon)
//	  change    = normalize(changes_since_present,        change_horizon)  // count, not duration
//	  unfinished = 1.0 if lifecycle_state == LifecycleStateUnresolved, else 0.0
//
//	Inhibition:
//	  cooldown  = 1 - normalize(elapsed_since_wake, wake_cooldown)
//	  budget    = budget (clamped [0,1], supplied by Core)
//
// Zero-value semantics:
//   - horizon <= 0 → signal = 0 (disabled)
//   - zero last_cognition_at → elapsed treated as infinite → saturated (1.0) if horizon > 0
//   - zero last_wake_at → cooldown = 0 (no recent wake = no inhibition)
func EvaluateSignals(now time.Time, cfg config.PulseConfig, pulseState PulseSnapshot, subjects []PulseSubjectState, inhibition InhibitionInputs) SignalSnapshot {
	// --- Activation: idle (global, subject-independent) ---
	idle := evaluateIdle(now, cfg, pulseState)

	// --- Activation: subject signals ---
	sigSubjects := make([]SubjectSignal, 0, len(subjects))
	for _, s := range subjects {
		ss := evaluateSubjectSignals(now, cfg, s)
		sigSubjects = append(sigSubjects, ss)
	}

	// --- Inhibition: cooldown ---
	cooldown := evaluateCooldown(now, cfg, pulseState)

	// --- Inhibition: budget (supplied by Core) ---
	budget := clampInhibition(inhibition.Budget)

	return SignalSnapshot{
		At:       now,
		Idle:     idle,
		Subjects: sigSubjects,
		Cooldown: cooldown,
		Budget:   budget,
	}
}

// evaluateIdle computes the subject-independent idle activation signal.
func evaluateIdle(now time.Time, cfg config.PulseConfig, pulseState PulseSnapshot) float64 {
	idleH := cfg.IdleHorizon
	if idleH <= 0 || !cfg.Enabled {
		return 0
	}

	var elapsed float64
	if pulseState.LastCognitionAt.IsZero() {
		// No cognition recorded yet → elapsed is unbounded → saturated if horizon > 0
		elapsed = math.Inf(1)
	} else {
		elapsed = now.Sub(pulseState.LastCognitionAt).Seconds()
		if elapsed < 0 {
			elapsed = 0 // backwards time → no elapsed
		}
	}

	return normalize(elapsed, float64(idleH))
}

// evaluateSubjectSignals computes neglect, change, and unfinished for one subject.
func evaluateSubjectSignals(now time.Time, cfg config.PulseConfig, s PulseSubjectState) SubjectSignal {
	neglect := evaluateNeglect(now, cfg, s)
	change := evaluateChange(cfg, s)
	unfinished := evaluateUnfinished(s)

	return SubjectSignal{
		SubjectID:  s.SubjectID,
		Neglect:    neglect,
		Change:     change,
		Unfinished: unfinished,
	}
}

// evaluateNeglect computes the neglect signal for one subject.
func evaluateNeglect(now time.Time, cfg config.PulseConfig, s PulseSubjectState) float64 {
	neglectH := cfg.NeglectHorizon
	if neglectH <= 0 || !cfg.Enabled {
		return 0
	}

	if s.LastPresentedAt.IsZero() {
		// Never presented → no neglect baseline
		return 0
	}

	elapsed := now.Sub(s.LastPresentedAt).Seconds()
	if elapsed < 0 {
		elapsed = 0 // backwards time → no elapsed
	}

	return normalize(elapsed, float64(neglectH))
}

// evaluateChange computes the count-based change signal for one subject.
// change_horizon is a COUNT (int64), not a duration.
func evaluateChange(cfg config.PulseConfig, s PulseSubjectState) float64 {
	changeH := cfg.ChangeHorizon
	if changeH <= 0 || !cfg.Enabled {
		return 0
	}

	changes := s.ChangesSincePresent
	if changes <= 0 {
		return 0
	}

	return normalize(float64(changes), float64(changeH))
}

// evaluateUnfinished returns 1.0 if the subject's lifecycle state is explicitly
// LifecycleStateUnresolved, else 0.0. No semantic inference or prose inspection.
func evaluateUnfinished(s PulseSubjectState) float64 {
	if s.LifecycleState == LifecycleStateUnresolved {
		return 1.0
	}
	return 0.0
}

// evaluateCooldown computes the cooldown inhibition signal.
// cooldown = 1 - normalize(elapsed_since_wake, wake_cooldown).
// 0 = no inhibition, 1 = fully inhibited. Zero cooldown → disabled.
func evaluateCooldown(now time.Time, cfg config.PulseConfig, pulseState PulseSnapshot) float64 {
	cd := cfg.WakeCooldown
	if cd <= 0 || !cfg.Enabled {
		return 0
	}

	if pulseState.LastSpontaneousWakeAt.IsZero() {
		// No wake recorded → no cooldown inhibition
		return 0
	}

	elapsed := now.Sub(pulseState.LastSpontaneousWakeAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}

	return 1 - normalize(elapsed, float64(cd))
}
