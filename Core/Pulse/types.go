package pulse

import "time"

// LifecycleStateUnresolved is the explicit lifecycle state value that causes
// the unfinished activation signal to saturate at 1.0. Core sets this on a
// subject when its lifecycle is unresolved. Pulse does not inspect semantic
// prose or infer this value.
const LifecycleStateUnresolved = "unresolved"

// PulseSnapshot is a race-safe read of the runner's global bookkeeping.
// LastCognitionAt is set by RecordCognition (called by Core on cognition
// events). LastSpontaneousWakeAt is set by RecordSpontaneousWake (M3+).
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
	RevisionAtLastPresent time.Time `json:"revision_at_last_present"`
	ChangesSincePresent   int64     `json:"changes_since_present"`
	LastSettledAt         time.Time `json:"last_settled_at"`
	LifecycleState        string    `json:"lifecycle_state"`
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
