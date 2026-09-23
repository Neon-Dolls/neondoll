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
//   - In M2, advertises runtime capabilities via its CapabilityRegistry
//
// Later milestones add authority (M3) and actual execution (M4).
type LocalBody struct {
	id   BodyID
	name string
	caps *CapabilityRegistry
}

// NewLocal creates the mandatory LocalBody with the canonical
// LocalBodyID. The identity is always "local::core" and cannot
// be overridden.
//
// The Local Body comes with a pre-registered set of capabilities:
//   - runtime.info / read — read runtime metadata about the Core
//
// Execution support is not yet available in M2 — capabilities are
// discoverable but not executable.
func NewLocal() *LocalBody {
	b := &LocalBody{
		id:   LocalBodyID,
		name: "local",
		caps: NewCapabilityRegistry(),
	}
	// Register M2 target capability: runtime.info / read.
	_ = b.caps.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Available:  true,
	})
	return b
}

// ID returns the stable identity of this LocalBody.
func (b *LocalBody) ID() BodyID { return b.id }

// Kind returns BodyKindLocal.
func (b *LocalBody) Kind() BodyKind { return BodyKindLocal }

// Name returns the human-readable label for this Body.
func (b *LocalBody) Name() string { return b.name }

// Describe returns the capabilities the LocalBody provides.
// In M2, this returns the pre-registered capabilities (runtime.info / read).
func (b *LocalBody) Describe() []Capability { return b.caps.All() }

// ResolveCapability checks whether a specific (capability ID, operation) pair
// is supported and available on this LocalBody. Delegates to the internal
// CapabilityRegistry.
func (b *LocalBody) ResolveCapability(capID string, operation string) error {
	return b.caps.Resolve(capID, operation)
}

// RegisterCapability declares a capability on this LocalBody, enforcing the
// M2 registry invariants. Delegates to the internal CapabilityRegistry.
func (b *LocalBody) RegisterCapability(cap Capability) error {
	return b.caps.Register(cap)
}

// Execute runs a capability on this LocalBody.
// In M1, execution is not yet available — returns an ErrExecutionNotAvailable.
func (b *LocalBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	return nil, fmt.Errorf("%w: capability %q", ErrExecutionNotAvailable, req.Capability)
}

// ErrExecutionNotAvailable is returned when a Body cannot execute
// capabilities because the execution subsystem is not yet wired.
// In M1, this is the expected response for all capabilities.
var ErrExecutionNotAvailable = fmt.Errorf("execution not available")
