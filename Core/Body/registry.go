package body

import "fmt"

// Registry holds all registered Bodies for a running Core. In Core 2 M1,
// exactly one mandatory Local Body is always present — the Registry enforces
// this invariant: a Registry must be created with at least one Local Body.
//
// The Registry is the "Body boundary" through which Core discovers and
// accesses Bodies. Later milestones extend it with remote Bodies, but
// in M1 it exists solely to prove that Core startup produces exactly
// one Local Body with a stable identity.
type Registry struct {
	bodies map[BodyID]Body
}

// NewRegistry creates a Registry containing exactly one Local Body
// (the mandatory in-process execution environment). Use Register to
// add further Bodies in later milestones.
//
// Registry creation represents "Core creates its Body boundary" —
// the Local Body always exists from the moment Core boots.
func NewRegistry() *Registry {
	r := &Registry{
		bodies: make(map[BodyID]Body),
	}
	local := NewLocal()
	r.bodies[local.ID()] = local
	return r
}

// Register adds a Body to the registry. Returns an error if a Body
// with the same ID is already registered. The mandatory Local Body
// (LocalBodyID) is always protected: any attempt to register another
// Body with its ID is rejected.
func (r *Registry) Register(b Body) error {
	if _, exists := r.bodies[b.ID()]; exists {
		return fmt.Errorf("body %q already registered", b.ID())
	}
	r.bodies[b.ID()] = b
	return nil
}

// Get retrieves a Body by its stable identity. Returns false if no
// Body with that ID is registered.
func (r *Registry) Get(id BodyID) (Body, bool) {
	b, ok := r.bodies[id]
	return b, ok
}

// Local returns the mandatory Local Body. This is guaranteed to
// always succeed for a properly created Registry.
func (r *Registry) Local() (Body, bool) {
	return r.Get(LocalBodyID)
}

// All returns all registered Bodies. The order is non-deterministic.
func (r *Registry) All() []Body {
	bodies := make([]Body, 0, len(r.bodies))
	for _, b := range r.bodies {
		bodies = append(bodies, b)
	}
	return bodies
}

// Count returns the number of registered Bodies.
func (r *Registry) Count() int {
	return len(r.bodies)
}

// GetCapabilities returns the capabilities declared by the Body with the
// given ID. Returns an error if no Body with that ID is registered.
func (r *Registry) GetCapabilities(id BodyID) ([]Capability, error) {
	b, ok := r.bodies[id]
	if !ok {
		return nil, fmt.Errorf("body %q not found", id)
	}
	return b.Describe(), nil
}

// ResolveCapability checks whether a specific (capability ID, operation) pair
// is supported and available on the Body identified by bodyID. Delegates to
// the Body's own ResolveCapability method.
//
// Returns the same sentinel errors as Body.ResolveCapability:
//   - nil if the capability exists, operation is declared, and Available
//   - ErrUnsupportedCapability if the capability ID is unknown
//   - ErrUnsupportedOperation if the capability exists but operation is not declared
//   - ErrUnavailable if the capability and operation are known but not available
func (r *Registry) ResolveCapability(bodyID BodyID, capID string, operation string) error {
	b, ok := r.bodies[bodyID]
	if !ok {
		return fmt.Errorf("body %q not found", bodyID)
	}
	return b.ResolveCapability(capID, operation)
}

// RegisterCapability declares a capability on the Body identified by bodyID,
// enforcing the M2 registry invariants. Delegates to the Body's own
// RegisterCapability method.
func (r *Registry) RegisterCapability(bodyID BodyID, cap Capability) error {
	b, ok := r.bodies[bodyID]
	if !ok {
		return fmt.Errorf("body %q not found", bodyID)
	}
	return b.RegisterCapability(cap)
}

// AllCapabilities returns all capabilities across every registered Body,
// grouped by BodyID. A Body with no capabilities contributes an entry
// with an empty slice.
func (r *Registry) AllCapabilities() map[BodyID][]Capability {
	result := make(map[BodyID][]Capability, len(r.bodies))
	for id, b := range r.bodies {
		result[id] = b.Describe()
	}
	return result
}
