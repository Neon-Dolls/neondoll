package pulse

import "time"

// PulseSnapshot is a race-safe read of the runner's global bookkeeping.
// LastCognitionAt and LastSpontaneousWakeAt are defined here for M1 but
// always return zero — they are set by later milestones (M2+, M3+).
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
