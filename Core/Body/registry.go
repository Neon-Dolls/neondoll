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

// Register adds a Body to the registry. If a Body with the same ID
// already exists, it is silently replaced.
func (r *Registry) Register(b Body) {
	r.bodies[b.ID()] = b
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

// ErrLocalBodyNotFound is returned when the mandatory Local Body
// is missing from a Registry (should never happen in normal use).
var ErrLocalBodyNotFound = fmt.Errorf("local body not found in registry")