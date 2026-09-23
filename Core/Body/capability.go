package body

import "fmt"

// CapabilityRegistry holds a set of registered capabilities for a Body.
// Each capability is identified by its Name, registered at Body
// construction time, and immutable after registration. The registry
// rejects duplicate registration.
type CapabilityRegistry struct {
	caps map[string]Capability
}

// NewCapabilityRegistry creates an empty CapabilityRegistry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{
		caps: make(map[string]Capability),
	}
}

// Register adds a capability. Returns an error if a capability with the
// same Name is already registered.
func (cr *CapabilityRegistry) Register(name, description string, params map[string]string) error {
	if _, exists := cr.caps[name]; exists {
		return fmt.Errorf("%w: %q", ErrCapabilityAlreadyRegistered, name)
	}
	cr.caps[name] = Capability{
		Name:        name,
		Description: description,
		Parameters:  params,
	}
	return nil
}

// Get retrieves a capability by name. Returns false if not found.
func (cr *CapabilityRegistry) Get(name string) (Capability, bool) {
	c, ok := cr.caps[name]
	return c, ok
}

// All returns all registered capabilities. Order is not guaranteed.
func (cr *CapabilityRegistry) All() []Capability {
	caps := make([]Capability, 0, len(cr.caps))
	for _, c := range cr.caps {
		caps = append(caps, c)
	}
	return caps
}

// Count returns the number of registered capabilities.
func (cr *CapabilityRegistry) Count() int {
	return len(cr.caps)
}

// ErrCapabilityAlreadyRegistered is returned when attempting to register
// a capability with a Name that already exists in the registry.
var ErrCapabilityAlreadyRegistered = fmt.Errorf("capability already registered")
