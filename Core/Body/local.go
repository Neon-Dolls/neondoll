package body

import "fmt"

// LocalBody is the mandatory in-process execution environment that every
// Core provides. Exactly one LocalBody exists in every running Core,
// registered at startup with the deterministic ID "local::core".
//
// The LocalBody:
//   - Has an explicit Body identity, independent of any transport
//   - Belongs to the running Core/Doll relationship, not to a session
//   - Implements the same semantic Body interface a remote Body will
//   - Does NOT fake WebSocket, Doll Link, or serialization
//   - In M1, returns nil capabilities and does not execute requests
//
// Later milestones add capability registration (M2), authority (M3),
// and actual execution (M4).
type LocalBody struct {
	id   BodyID
	name string
}

// LocalOption configures the LocalBody at construction.
type LocalOption func(*LocalBody)

// NewLocal creates the mandatory LocalBody with default settings.
// By default, its ID is LocalBodyID and its name is "local".
func NewLocal(opts ...LocalOption) *LocalBody {
	b := &LocalBody{
		id:   LocalBodyID,
		name: "local",
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// ID returns the stable identity of this LocalBody.
func (b *LocalBody) ID() BodyID { return b.id }

// Kind returns BodyKindLocal.
func (b *LocalBody) Kind() BodyKind { return BodyKindLocal }

// Name returns the human-readable label for this Body.
func (b *LocalBody) Name() string { return b.name }

// Describe returns the capabilities the LocalBody provides.
// In M1, this is always empty — capability registration is M2.
func (b *LocalBody) Describe() []Capability { return nil }

// Execute runs a capability on this LocalBody.
// In M1, execution is not yet available — returns an ErrExecutionNotAvailable.
func (b *LocalBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	return nil, fmt.Errorf("%w: capability %q", ErrExecutionNotAvailable, req.Capability)
}

// ErrExecutionNotAvailable is returned when a Body cannot execute
// capabilities because the execution subsystem is not yet wired.
// In M1, this is the expected response for all capabilities.
var ErrExecutionNotAvailable = fmt.Errorf("execution not available")