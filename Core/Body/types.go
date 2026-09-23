// Package body defines the Core Body boundary — the semantic types that
// represent every Body (local, remote, or otherwise) within a running Core.
//
// The Body boundary is the interface between Doll Mind and the environment.
// It is NOT a transport connection, a session, or a WebSocket link. A Body
// is an execution provider: it declares capabilities and performs operations.
//
// Core 2 Milestone 1 introduces the minimal vocabulary. Capability
// registration (M2), authority (M3), and execution (M4) build on it. In M1, types exist and the mandatory Local Body can be
// identified by identity and kind.
package body

import (
	"errors"
	"fmt"
)

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

// Sentinel errors for capability resolution.
var (
	// ErrUnsupportedCapability is returned when a capability ID is not
	// declared by any registered capability on a Body.
	ErrUnsupportedCapability = errors.New("unsupported capability")

	// ErrUnsupportedOperation is returned when a capability ID exists but
	// the requested operation is not among its declared operations.
	ErrUnsupportedOperation = errors.New("unsupported operation")

	// ErrUnavailable is returned when a capability and operation are known
	// but the capability is marked as not currently available.
	ErrUnavailable = errors.New("capability unavailable")
)

// Capability describes a named capability a Body can perform. A capability
// owns one or more distinct operations and declares its availability
// independently of any future authority layer.
//
//	ID:         "runtime.info"
//	Operations: ["read"]
//	Available:  true
//
// M2 introduces explicit capability registration with operation-level
// resolution. Execution and authority come in later milestones.
type Capability struct {
	// ID is the canonical semantic identity of this capability within
	// the Body. Convention: "namespace:action" (e.g. "runtime.info").
	ID string `json:"id"`

	// Operations is the set of distinct operations this capability
	// supports (e.g. ["read"]). At least one operation is required.
	Operations []string `json:"operations"`

	// Available indicates whether this capability can be used right now.
	// A capability may exist but be unavailable (e.g. hardware offline).
	Available bool `json:"available"`

	// Constraints describes capability-specific constraints, if any.
	Constraints map[string]string `json:"constraints,omitempty"`

	// Metadata carries additional discovery-time information about
	// this capability.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ExecutionRequest is the canonical request that crosses the guarded Core
// execution boundary (M4). It carries everything needed to identify,
// authorize, and execute one operation: the execution ID used to correlate
// the terminal result, the requesting Doll, the target Body, the capability,
// the operation, and the arguments.
//
// The exact Arguments are presented to authority evaluation and then passed
// unchanged to the Body operation — Guard.Execute does not authorize one
// request and execute a mutated request.
type ExecutionRequest struct {
	// ExecutionID is the stable, opaque correlation identity of this
	// request. Every terminal result carries the same ID. Execution IDs
	// are opaque: authority is never derived from them and they do not
	// encode Body locality or provider details.
	ExecutionID string `json:"execution_id"`

	// Doll identifies the Doll requesting the operation.
	Doll string `json:"doll"`

	// Body is the target Body that would perform the operation.
	Body BodyID `json:"body"`

	// Capability is the capability ID requested (e.g. "runtime.info").
	Capability string `json:"capability"`

	// Operation is the operation requested (e.g. "read").
	Operation string `json:"operation"`

	// Arguments holds execution-specific key-value inputs. These exact
	// arguments are what authority evaluates and what the Body executes.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ExecutionStatus represents the terminal state of a capability execution.
// M4 defines the Core 2 terminal outcome vocabulary:
//
//	success | denied | unsupported | invalid | unavailable | failed
//
// These outcomes stay distinct. Denial is not failure, an unsupported
// capability/operation is not failure, an unavailable capability is not
// failure, and a malformed request is not failure.
type ExecutionStatus string

const (
	// StatusSuccess is the terminal outcome of an authorized execution
	// that completed its operation.
	StatusSuccess ExecutionStatus = "success"

	// StatusDenied is the terminal outcome when authority refused the
	// request. Never collapse into StatusFailed.
	StatusDenied ExecutionStatus = "denied"

	// StatusUnsupported is the terminal outcome when the capability or
	// operation is unknown. Never collapse into StatusFailed.
	StatusUnsupported ExecutionStatus = "unsupported"

	// StatusInvalid is the terminal outcome when the request is
	// structurally invalid and can never execute. Never collapse into
	// StatusFailed.
	StatusInvalid ExecutionStatus = "invalid"

	// StatusUnavailable is the terminal outcome when the capability and
	// operation are known but not currently available. Never collapse
	// into StatusFailed.
	StatusUnavailable ExecutionStatus = "unavailable"

	// StatusFailed is the terminal outcome when an authorized invocation
	// failed during execution.
	StatusFailed ExecutionStatus = "failed"

	// StatusPending and StatusRunning are the pre-terminal M1/M2 lifecycle
	// states. M4 execution is synchronous from the Core caller's
	// perspective; these are retained for vocabulary compatibility.
	StatusPending ExecutionStatus = "pending"
	StatusRunning ExecutionStatus = "running"

	// StatusCompleted is a legacy alias for StatusSuccess, retained for
	// compatibility with M1/M2 vocabulary.
	StatusCompleted ExecutionStatus = "success"
)

// ExecutionResult represents the outcome of a capability execution.
// It carries the execution ID of the request it answers and a terminal
// status from the M4 outcome vocabulary:
// success | denied | unsupported | invalid | unavailable | failed.
type ExecutionResult struct {
	// ExecutionID correlates this result with the request that produced it.
	ExecutionID string `json:"execution_id"`

	// Status is the terminal outcome.
	Status ExecutionStatus `json:"status"`

	// Output holds the operation output. Structured payloads (e.g.
	// runtime.info / read) are JSON documents.
	Output string `json:"output,omitempty"`

	// ErrorCode is a machine-usable reason code (e.g. "no_rule",
	// "unsupported_capability", "invocation_failed").
	ErrorCode string `json:"error_code,omitempty"`

	// Error is an optional human-readable explanation.
	Error string `json:"error,omitempty"`
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

	// ResolveCapability checks whether a specific (capability ID, operation)
	// pair is supported and available on this Body.
	//
	// Returns:
	//   - nil if the capability exists, operation is declared, and Available
	//   - ErrUnsupportedCapability if the capability ID is unknown
	//   - ErrUnsupportedOperation if the capability exists but operation is not declared
	//   - ErrUnavailable if the capability and operation are known but not available
	ResolveCapability(capID string, operation string) error

	// RegisterCapability declares a capability on this Body. Enforces the
	// M2 registry invariants (non-empty ID, at least one operation, no
	// duplicate IDs, no duplicate operations within a capability).
	RegisterCapability(cap Capability) error

	// Execute runs a capability synchronously and returns the result.
	// The request is already resolved and authorized by Core before it
	// reaches the Body (see Guard.Execute). This is the Body-side
	// invocation primitive, NOT the Core authorization boundary —
	// production Core callers use the guarded Guard.Execute path so that
	// no capability invocation occurs without capability resolution and
	// authority evaluation first.
	Execute(req ExecutionRequest) (*ExecutionResult, error)
}
