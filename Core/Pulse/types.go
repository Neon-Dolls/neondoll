package pulse

import (
	"context"
	"time"
)

// LifecycleStateUnresolved is the explicit lifecycle state value that causes
// the unfinished activation signal to saturate at 1.0. Core sets this on a
// subject when its lifecycle is unresolved. Pulse does not inspect semantic
// prose or infer this value.
const LifecycleStateUnresolved = "unresolved"

// PulseSnapshot is a race-safe read of the runner's global bookkeeping.
// LastCognitionAt is set by RecordCognition (called by Core on cognition
// events). LastSpontaneousWakeAt is available for M3+ spontaneous-wake
// tracking; in M2 it remains zero (no mutation path).
type PulseSnapshot struct {
	TickCount             int64     `json:"tick_count"`
	LastTickAt            time.Time `json:"last_tick_at"`
	LastCognitionAt       time.Time `json:"last_cognition_at"`
	LastSpontaneousWakeAt time.Time `json:"last_spontaneous_wake_at"`
}

// PulseResult records what one Pulse evaluation produced.
type PulseResult struct {
	TickCount     int64     `json:"tick_count"`
	At            time.Time `json:"at"`
	BackwardsTime bool      `json:"backwards_time"`
}

// PulseSubjectState is the bookkeeping for one subject that Pulse observes.
// Pulse receives these from Core; it does NOT own, mutate, or persist them.
// LifecycleState is an opaque string whose only Pulse-meaningful value is
// LifecycleStateUnresolved (sets unfinished=1). Pulse never infers state
// from semantic prose.
type PulseSubjectState struct {
	SubjectID             string    `json:"subject_id"`
	LastPresentedAt       time.Time `json:"last_presented_at"`
	RevisionAtLastPresent int64     `json:"revision_at_last_present"`
	ChangesSincePresent   int64     `json:"changes_since_present"`
	LastSettledAt         time.Time `json:"last_settled_at"`
	LifecycleState        string    `json:"lifecycle_state"`
}

// MarkPresented sets last_presented_at to at if at is strictly after the
// current value (forward-time update only). No-op when at is zero.
// This method is used by the Pulse runner when the admitted Cognition Run
// is presented to Mind, recording that the subject's current state was
// offered for processing.
func (s *PulseSubjectState) MarkPresented(at time.Time) {
	if at.IsZero() {
		return
	}
	if at.After(s.LastPresentedAt) {
		s.LastPresentedAt = at
	}
}

// MarkSettled sets last_settled_at to at if at is strictly after the current
// value (forward-time update only). No-op when at is zero.
// This method is used by the Pulse runner after a cognition run completes
// successfully, recording that the subject's state was fully processed.
// Failed or aborted cognition MUST NOT call MarkSettled — call
// MarkPresented alone so that neglect signals reflect the presentation
// without a successful settling.
func (s *PulseSubjectState) MarkSettled(at time.Time) {
	if at.IsZero() {
		return
	}
	if at.After(s.LastSettledAt) {
		s.LastSettledAt = at
	}
}

// InhibitionInputs is the narrow input seam through which Core supplies
// inhibition values. Pulse does not invent, calculate, or budget-manage
// these — it only reads them. Budget must be in [0, 1]; values outside
// are clamped.
type InhibitionInputs struct {
	Budget float64 `json:"budget"`
}

// SubjectSignal holds the three activation signals evaluated for one subject.
type SubjectSignal struct {
	SubjectID  string  `json:"subject_id"`
	Neglect    float64 `json:"neglect"`
	Change     float64 `json:"change"`
	Unfinished float64 `json:"unfinished"`
}

// SignalSnapshot is a complete, inspectable record of all evaluated signals
// at one point in time. Activation signals (idle, neglect, change, unfinished)
// and inhibition signals (cooldown, budget) are kept separate.
type SignalSnapshot struct {
	At       time.Time       `json:"at"`
	Idle     float64         `json:"idle"`
	Subjects []SubjectSignal `json:"subjects"`
	Cooldown float64         `json:"cooldown"`
	Budget   float64         `json:"budget"`
}

// SubjectActivation records which signals from a specific subject contributed
// to an opportunity's pressure calculation. A SubjectActivation is emitted for
// every subject with at least one non-zero activation signal. The global idle
// signal is NOT represented as a SubjectActivation — it is always a top-level
// field on OpportunitySnapshot.
type SubjectActivation struct {
	SubjectID  string  `json:"subject_id"`
	Neglect    float64 `json:"neglect,omitempty"`
	Change     float64 `json:"change,omitempty"`
	Unfinished float64 `json:"unfinished,omitempty"`
}

// GuardReason is a typed constant describing why a guard blocked or allowed
// an opportunity evaluation.
type GuardReason string

const (
	GuardReasonMinWakeSpacing GuardReason = "min_wake_spacing"
	GuardReasonCognitionRun   GuardReason = "cognition_run_active"
)

// GuardResult records one hard guard check for an opportunity evaluation.
type GuardResult struct {
	Reason  GuardReason `json:"reason"`
	Blocked bool        `json:"blocked"`
	Message string      `json:"message"`
}

// OpportunitySnapshot is the complete, inspectable record of one stochastic
// opportunity evaluation by Pulse. It captures pressure calculation, soft
// inhibition, hard guard checks, and the stochastic sample.
type OpportunitySnapshot struct {
	EvaluatedAt       time.Time           `json:"evaluated_at"`
	Type              string              `json:"type"` // always "spontaneous"
	Pressure          float64             `json:"pressure"`
	EffectivePressure float64             `json:"effective_pressure"`
	ActivationSignals []float64           `json:"activation_signals"`
	Inhibition        InhibitionBreakdown `json:"inhibition"`
	Subjects          []SubjectActivation `json:"subjects"`
	Eligible          bool                `json:"eligible"`
	Guards            []GuardResult       `json:"guards"`
	RandomSample      *float64            `json:"random_sample,omitempty"`
	Opportunity       bool                `json:"opportunity"`
}

// InhibitionBreakdown records the soft-inhibition factors applied to pressure.
type InhibitionBreakdown struct {
	Cooldown float64 `json:"cooldown"`
	Budget   float64 `json:"budget"`
}

// RNG abstracts random number generation so Pulse never calls math/rand
// directly. The Float64 method returns a value in [0.0, 1.0).
type RNG interface {
	Float64() float64
}

// PulseWake is the operational evidence that admits a spontaneous cognition
// run. It is machine-readable runtime context, not semantic Doll State.
// PulseWake is produced by the Runner and consumed by DollMind via the
// MindEntrance interface.
type PulseWake struct {
	AdmittedAt        time.Time           `json:"admitted_at"`
	Pressure          float64             `json:"pressure"`
	EffectivePressure float64             `json:"effective_pressure"`
	ActivationSignals []float64           `json:"activation_signals"`
	Inhibition        InhibitionBreakdown `json:"inhibition"`
	Subjects          []SubjectActivation `json:"subjects"`
	RandomSample      float64             `json:"random_sample"`
}

// MindEntrance is the interface Pulse uses to admit a spontaneous cognition
// run into DollMind. Implementations must not hold the Pulse state mutex
// through the call.
type MindEntrance interface {
	// EnterPulseWake admits a spontaneous Pulse wake into the cognition
	// pipeline. It returns an error only for actual failures (inference
	// errors, parse errors); matters=false is NOT an error.
	EnterPulseWake(ctx context.Context, wake PulseWake) error
}

// ensureSubjects returns a non-nil empty slice when in is nil, allowing
// JSON serialization to produce "subjects": [] rather than "subjects": null.
func ensureSubjects(in []SubjectActivation) []SubjectActivation {
	if in == nil {
		return []SubjectActivation{}
	}
	return in
}
