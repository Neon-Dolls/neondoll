// Package body defines the Core Body boundary — the semantic types that
// represent every Body (local, remote, or otherwise) within a running Core.
//
// The Body boundary is the interface between Doll Mind and the environment.
// It is NOT a transport connection, a session, or a WebSocket link. A Body
// is an execution provider: it declares capabilities and performs operations.
//
// Core 2 Milestone 1 introduces the minimal vocabulary. Actual capability
// registration (M2), authority (M3), and execution (M4) come in later
// milestones. In M1, types exist and the mandatory Local Body can be
// identified by identity and kind.
package body

import "fmt"

// BodyID uniquely identifies a Body within a running Core. A BodyID is
// stable for the lifetime of the Core instance and survives configuration
// changes, but does NOT survive export/import — it is not part of Doll
// State. The Doll does not own her Bodies; the Core does.
type BodyID string

// LocalBodyID is the deterministic identity of the mandatory in-process
// Local Body. Every running Core always has exactly one Local Body.
const LocalBodyID BodyID = "local::core"

// BodyKind classifies Bodies by their nature.
type BodyKind string

const (
	// BodyKindLocal identifies the in-process Local Body. Exactly one
	// exists in every running Core.
	BodyKindLocal BodyKind = "local"
)

// Capability describes a single operation a Body can perform. A Body
// declares its capabilities in Describe(). The capability name is used
// as the execution key in ExecutionRequest.
//
// In M1, capabilities are a descriptive type only — no capabilities are
// registered, resolved, or executed. M2 introduces the Capability Registry.
type Capability struct {
	// Name is the unique key for this capability within the Body.
	// Convention: "namespace:action" (e.g. "file:read", "shell:exec").
	Name string `json:"name"`

	// Description is a human-readable explanation of what this capability does.
	Description string `json:"description,omitempty"`

	// Parameters describes expected parameter names and types, if known.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ExecutionRequest is the input to execute a Body capability.
// M1 defines the shape only — execution is not wired until M4.
type ExecutionRequest struct {
	// Capability is the Name of the capability to execute.
	Capability string `json:"capability"`

	// Parameters holds execution-specific key-value inputs.
	Parameters map[string]any `json:"parameters,omitempty"`
}

// ExecutionStatus represents the state of a capability execution.
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusDenied    ExecutionStatus = "denied"
)

// ExecutionResult represents the outcome of a capability execution.
type ExecutionResult struct {
	Status ExecutionStatus `json:"status"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// String returns a human-readable representation.
func (r ExecutionResult) String() string {
	if r.Error != "" {
		return fmt.Sprintf("%s: %s", r.Status, r.Error)
	}
	return fmt.Sprintf("%s: %s", r.Status, r.Output)
}

// Body is the interface that all Bodies — local, remote, and future kinds —
// implement. It represents the environment boundary for the Doll Mind:
// a Body declares capabilities and accepts execution requests.
//
// A Body is NOT a transport connection, a WebSocket session, an Interaction
// Service handler, or a Doll Link client. It is a capabilities-and-execution
// provider with a stable identity within the Core.
type Body interface {
	// ID returns the stable identity of this Body within the Core.
	ID() BodyID

	// Kind returns the Body kind (local, remote, etc.).
	Kind() BodyKind

	// Name returns a human-readable label for this Body.
	Name() string

	// Describe returns the capabilities this Body provides.
	// In M1, the Local Body returns an empty slice.
	Describe() []Capability

	// Execute runs a capability synchronously and returns the result.
	// In M1-M3, this is not wired — Local Execute returns an error
	// indicating execution is not yet available.
	Execute(req ExecutionRequest) (*ExecutionResult, error)
}
