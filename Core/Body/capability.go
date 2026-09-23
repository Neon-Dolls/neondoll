// Package body capability — capability registration, discovery, and resolution.
//
// M2 introduces the CapabilityRegistry with operation-level resolution.
// Each capability owns one or more distinct operations and declares its
// availability independently of any future authority layer.

package body

import "fmt"

// CapabilityRegistry manages a Body's capabilities with invariant enforcement.
//
// Invariants enforced by Register:
//   - capability ID must not be empty
//   - each capability must have at least one operation
//   - capability IDs are unique within the registry
//   - operation names are unique within a capability
//
// Discovery methods (All, Get) return defensive copies so callers cannot
// mutate the registry's internal state.
type CapabilityRegistry struct {
	caps map[string]Capability
}

// NewCapabilityRegistry creates an empty capability registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{
		caps: make(map[string]Capability),
	}
}

// Register adds a capability to the registry, enforcing M2 invariants.
// Returns an error if any invariant is violated; the registry is not modified.
func (cr *CapabilityRegistry) Register(c Capability) error {
	if c.ID == "" {
		return fmt.Errorf("capability ID must not be empty")
	}
	if len(c.Operations) == 0 {
		return fmt.Errorf("capability %q has no operations", c.ID)
	}
	if _, exists := cr.caps[c.ID]; exists {
		return fmt.Errorf("capability %q already registered", c.ID)
	}

	// Check for duplicate operation names within this capability.
	seen := make(map[string]bool, len(c.Operations))
	for _, op := range c.Operations {
		if seen[op] {
			return fmt.Errorf("duplicate operation %q in capability %q", op, c.ID)
		}
		seen[op] = true
	}

	// Store a defensive copy.
	ops := make([]string, len(c.Operations))
	copy(ops, c.Operations)
	var constraints map[string]string
	if c.Constraints != nil {
		constraints = make(map[string]string, len(c.Constraints))
		for k, v := range c.Constraints {
			constraints[k] = v
		}
	}
	var metadata map[string]string
	if c.Metadata != nil {
		metadata = make(map[string]string, len(c.Metadata))
		for k, v := range c.Metadata {
			metadata[k] = v
		}
	}

	cr.caps[c.ID] = Capability{
		ID:          c.ID,
		Operations:  ops,
		Available:   c.Available,
		Constraints: constraints,
		Metadata:    metadata,
	}
	return nil
}

// Has reports whether a capability with the given ID is registered.
func (cr *CapabilityRegistry) Has(id string) bool {
	_, ok := cr.caps[id]
	return ok
}

// Get returns a defensive copy of the capability with the given ID.
// The boolean is false if no such capability is registered.
func (cr *CapabilityRegistry) Get(id string) (Capability, bool) {
	c, ok := cr.caps[id]
	if !ok {
		return Capability{}, false
	}
	return cloneCapability(c), true
}

// All returns defensive copies of every registered capability.
func (cr *CapabilityRegistry) All() []Capability {
	if len(cr.caps) == 0 {
		return nil
	}
	result := make([]Capability, 0, len(cr.caps))
	for _, c := range cr.caps {
		result = append(result, cloneCapability(c))
	}
	return result
}

// Count returns the number of registered capabilities.
func (cr *CapabilityRegistry) Count() int {
	return len(cr.caps)
}

// Resolve checks whether a specific (capability ID, operation) pair is
// supported and available.
//
// Returns:
//   - nil if the capability exists, operation is declared, and Available is true
//   - ErrUnsupportedCapability if no capability with the given ID is registered
//   - ErrUnsupportedOperation if the capability exists but the operation is
//     not among its declared operations
//   - ErrUnavailable if the capability exists, the operation is declared,
//     but Available is false
func (cr *CapabilityRegistry) Resolve(capID string, op string) error {
	c, ok := cr.caps[capID]
	if !ok {
		return ErrUnsupportedCapability
	}

	found := false
	for _, o := range c.Operations {
		if o == op {
			found = true
			break
		}
	}
	if !found {
		return ErrUnsupportedOperation
	}

	if !c.Available {
		return ErrUnavailable
	}

	return nil
}

// cloneCapability returns a deep copy of the given Capability.
func cloneCapability(c Capability) Capability {
	ops := make([]string, len(c.Operations))
	copy(ops, c.Operations)

	var constraints map[string]string
	if c.Constraints != nil {
		constraints = make(map[string]string, len(c.Constraints))
		for k, v := range c.Constraints {
			constraints[k] = v
		}
	}

	var metadata map[string]string
	if c.Metadata != nil {
		metadata = make(map[string]string, len(c.Metadata))
		for k, v := range c.Metadata {
			metadata[k] = v
		}
	}

	return Capability{
		ID:          c.ID,
		Operations:  ops,
		Available:   c.Available,
		Constraints: constraints,
		Metadata:    metadata,
	}
}
