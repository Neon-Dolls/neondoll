package pulse

import (
	"time"
)

// PulseSubject is something Pulse evaluates. In M1 this is just an identifier
// with no semantics — no inference, no Doll Mind, no wake events.
type PulseSubject string

// PulseSnapshot is a read-only, thread-safe view of Pulse state at a point in time.
type PulseSnapshot struct {
	TickCount      int64     `json:"tick_count"`
	LastTickTime   time.Time `json:"last_tick_time"`
	EvaluatedCount int64     `json:"evaluated_count"`
}

// TemporalObservation records a single Pulse evaluation tick.
type TemporalObservation struct {
	At        time.Time    `json:"at"`
	Subject   PulseSubject `json:"subject"`
	TickIndex int64        `json:"tick_index"`
}

// PulseResult is the deterministic output of a single Pulse evaluation.
type PulseResult struct {
	TickIndex     int64                 `json:"tick_index"`
	At            time.Time             `json:"at"`
	Subjects      []PulseSubject        `json:"subjects"`
	Observations  []TemporalObservation `json:"observations"`
	BackwardsTime bool                  `json:"backwards_time"`
}

// PulseStatus represents the operational status of a Pulse runner.
type PulseStatus string

const (
	PulseStatusStopped PulseStatus = "stopped"
	PulseStatusRunning PulseStatus = "running"
)
