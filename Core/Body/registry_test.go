package body

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewRegistry_ContainsLocalBody(t *testing.T) {
	r := NewRegistry()
	if r.Count() != 1 {
		t.Fatalf("expected 1 body in registry, got %d", r.Count())
	}
}

func TestNewRegistry_LocalBodyHasCorrectID(t *testing.T) {
	r := NewRegistry()
	b, ok := r.Local()
	if !ok {
		t.Fatal("expected Local Body in registry")
	}
	if b.ID() != LocalBodyID {
		t.Errorf("LocalBody.ID() = %q, want %q", b.ID(), LocalBodyID)
	}
}

func TestNewRegistry_LocalBodyHasCorrectKind(t *testing.T) {
	r := NewRegistry()
	b, _ := r.Local()
	if b.Kind() != BodyKindLocal {
		t.Errorf("expected BodyKindLocal, got %v", b.Kind())
	}
}

func TestNewRegistry_AllIncludesLocal(t *testing.T) {
	r := NewRegistry()
	all := r.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 body from All(), got %d", len(all))
	}
	if all[0].ID() != LocalBodyID {
		t.Errorf("expected body with ID %q, got %q", LocalBodyID, all[0].ID())
	}
}

func TestRegistry_RegisterAddsBody(t *testing.T) {
	r := NewRegistry()

	testBody := NewCustomBody(BodyID("test:custom"), BodyKindLocal, "test")
	err := r.Register(testBody)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if r.Count() != 2 {
		t.Fatalf("expected 2 bodies, got %d", r.Count())
	}

	b, ok := r.Get("test:custom")
	if !ok {
		t.Fatal("expected to find custom body")
	}
	if b.Name() != "test" {
		t.Errorf("expected name 'test', got %q", b.Name())
	}
}

func TestRegistry_RegisterRejectsLocalBodyReplacement(t *testing.T) {
	r := NewRegistry()

	// Attempt to register another Body claiming LocalBodyID.
	impostor := NewCustomBody(LocalBodyID, BodyKindLocal, "impostor")
	err := r.Register(impostor)
	if err == nil {
		t.Fatal("expected error when replacing Local Body, got nil")
	}

	// Prove the original Local Body remains intact.
	b, ok := r.Local()
	if !ok {
		t.Fatal("original Local Body must still be accessible")
	}
	if b.Name() != "local" {
		t.Errorf("original Local Body name = %q, want %q", b.Name(), "local")
	}
	if r.Count() != 1 {
		t.Errorf("registry count = %d, want 1 (no replacement occurred)", r.Count())
	}
}

func TestRegistry_GetMissingReturnsFalse(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get(BodyID("nonexistent"))
	if ok {
		t.Fatal("expected false for missing body")
	}
}

func TestNewRegistry_CountExactlyOne(t *testing.T) {
	r1 := NewRegistry()
	r2 := NewRegistry()
	if r1.Count() != 1 || r2.Count() != 1 {
		t.Fatalf("every Registry must start with exactly 1 body, got %d and %d",
			r1.Count(), r2.Count())
	}
}

func TestNewRegistry_LocalBodyHasCapabilities(t *testing.T) {
	r := NewRegistry()
	b, _ := r.Local()
	caps := b.Describe()
	if len(caps) != 1 {
		t.Fatalf("M2 LocalBody should have 1 capability, got %d", len(caps))
	}
	if caps[0].ID != "runtime.info" {
		t.Errorf("capability ID = %q, want %q", caps[0].ID, "runtime.info")
	}
	if len(caps[0].Operations) != 2 || caps[0].Operations[0] != "read" || caps[0].Operations[1] != "list" {
		t.Errorf("capability Operations = %v, want [read list]", caps[0].Operations)
	}
	if !caps[0].Available {
		t.Error("capability Available = false, want true")
	}
}

func TestNewRegistry_IdentityNotTransport(t *testing.T) {
	r := NewRegistry()
	b, _ := r.Local()
	id := string(b.ID())
	transportTerms := []string{"ws", "socket", "tcp", "session", "connection", "transport"}
	for _, term := range transportTerms {
		if strings.Contains(strings.ToLower(id), term) {
			t.Errorf("LocalBody.ID() contains transport term %q: %q", term, id)
		}
	}
}

func TestRegistry_GetCapabilitiesReturnsLocalCaps(t *testing.T) {
	r := NewRegistry()
	caps, err := r.GetCapabilities(LocalBodyID)
	if err != nil {
		t.Fatalf("GetCapabilities(LocalBodyID) failed: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(caps))
	}
	if caps[0].ID != "runtime.info" {
		t.Errorf("capability ID = %q, want %q", caps[0].ID, "runtime.info")
	}
	if len(caps[0].Operations) != 2 || caps[0].Operations[0] != "read" || caps[0].Operations[1] != "list" {
		t.Errorf("capability Operations = %v, want [read list]", caps[0].Operations)
	}
}

func TestRegistry_GetCapabilitiesMissingBody(t *testing.T) {
	r := NewRegistry()
	_, err := r.GetCapabilities("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing Body")
	}
}

func TestRegistry_GetCapabilitiesRegisteredBody(t *testing.T) {
	r := NewRegistry()
	custCap := Capability{
		ID:         "custom:test",
		Operations: []string{"do-thing"},
		Available:  true,
	}
	cust := NewCustomBodyWithCaps("test:custom", BodyKindLocal, "custom",
		[]Capability{custCap})
	err := r.Register(cust)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	caps, err := r.GetCapabilities("test:custom")
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(caps))
	}
	if caps[0].ID != "custom:test" {
		t.Errorf("capability ID = %q, want %q", caps[0].ID, "custom:test")
	}
	if len(caps[0].Operations) != 1 || caps[0].Operations[0] != "do-thing" {
		t.Errorf("capability Operations = %v, want [do-thing]", caps[0].Operations)
	}
}

func TestRegistry_AllCapabilitiesIncludesLocal(t *testing.T) {
	r := NewRegistry()
	all := r.AllCapabilities()
	if len(all) != 1 {
		t.Fatalf("expected 1 Body in AllCapabilities, got %d", len(all))
	}
	localCaps, ok := all[LocalBodyID]
	if !ok {
		t.Fatal("AllCapabilities must include LocalBodyID key")
	}
	if len(localCaps) != 1 {
		t.Fatalf("expected 1 Local capability, got %d", len(localCaps))
	}
}

func TestRegistry_AllCapabilitiesMultipleBodies(t *testing.T) {
	r := NewRegistry()
	cust := NewCustomBody("test:custom", BodyKindLocal, "custom")
	err := r.Register(cust)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	all := r.AllCapabilities()
	if len(all) != 2 {
		t.Fatalf("expected 2 Bodies in AllCapabilities, got %d", len(all))
	}
	// Custom body has nil caps — should still appear with empty slice.
	custCaps, ok := all["test:custom"]
	if !ok {
		t.Fatal("AllCapabilities must include custom Body key")
	}
	if len(custCaps) != 0 {
		t.Errorf("custom body should have 0 capabilities, got %d", len(custCaps))
	}
}

func TestRegistry_ResolveCapabilityThroughBodyBoundary(t *testing.T) {
	r := NewRegistry()

	// AC1: runtime.info / read on the Local Body → supported + available.
	if err := r.ResolveCapability(LocalBodyID, "runtime.info", "read"); err != nil {
		t.Errorf("ResolveCapability(local, runtime.info, read) = %v, want nil", err)
	}

	// AC2: unknown capability → explicit unsupported capability.
	if err := r.ResolveCapability(LocalBodyID, "nonexistent", "read"); err != ErrUnsupportedCapability {
		t.Errorf("ResolveCapability(local, nonexistent, read) = %v, want ErrUnsupportedCapability", err)
	}

	// AC3: known capability, unknown operation → explicit unsupported operation.
	if err := r.ResolveCapability(LocalBodyID, "runtime.info", "write"); err != ErrUnsupportedOperation {
		t.Errorf("ResolveCapability(local, runtime.info, write) = %v, want ErrUnsupportedOperation", err)
	}
}

func TestRegistry_ResolveCapabilityUnavailableCustomBody(t *testing.T) {
	r := NewRegistry()
	blocked := Capability{
		ID:         "test:blocked",
		Operations: []string{"do-thing"},
		Available:  false,
	}
	cust := NewCustomBodyWithCaps("test:offline", BodyKindLocal, "offline",
		[]Capability{blocked})
	if err := r.Register(cust); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// AC4: known but unavailable capability → explicit unavailable.
	if err := r.ResolveCapability("test:offline", "test:blocked", "do-thing"); err != ErrUnavailable {
		t.Errorf("ResolveCapability(offline, test:blocked, do-thing) = %v, want ErrUnavailable", err)
	}
}

func TestRegistry_ResolveCapabilityMissingBody(t *testing.T) {
	r := NewRegistry()
	err := r.ResolveCapability("no:such:body", "runtime.info", "read")
	if err == nil {
		t.Fatal("expected error for missing Body")
	}
}

// NewCustomBody creates a simple Body for testing that satisfies the Body interface.
func NewCustomBody(id BodyID, kind BodyKind, name string) Body {
	return &customBody{id: id, kind: kind, name: name}
}

// NewCustomBodyWithCaps is like NewCustomBody but includes capabilities.
func NewCustomBodyWithCaps(id BodyID, kind BodyKind, name string, caps []Capability) Body {
	return &customBody{id: id, kind: kind, name: name, caps: caps}
}

type customBody struct {
	id   BodyID
	kind BodyKind
	name string
	caps []Capability
}

func (c *customBody) ID() BodyID     { return c.id }
func (c *customBody) Kind() BodyKind { return c.kind }
func (c *customBody) Name() string   { return c.name }
func (c *customBody) Describe() []Capability {
	if c.caps == nil {
		return nil
	}
	out := make([]Capability, len(c.caps))
	for i, cap := range c.caps {
		out[i] = cap
		if cap.Operations != nil {
			out[i].Operations = append([]string(nil), cap.Operations...)
		}
	}
	return out
}
func (c *customBody) ResolveCapability(capID string, operation string) error {
	for _, cap := range c.caps {
		if cap.ID != capID {
			continue
		}
		for _, op := range cap.Operations {
			if op == operation {
				if !cap.Available {
					return ErrUnavailable
				}
				return nil
			}
		}
		return ErrUnsupportedOperation
	}
	return ErrUnsupportedCapability
}
func (c *customBody) RegisterCapability(cap Capability) error {
	if cap.ID == "" {
		return fmt.Errorf("capability ID must not be empty")
	}
	if len(cap.Operations) == 0 {
		return fmt.Errorf("capability %q has no operations", cap.ID)
	}
	for _, existing := range c.caps {
		if existing.ID == cap.ID {
			return fmt.Errorf("capability %q already registered", cap.ID)
		}
	}
	seen := make(map[string]bool, len(cap.Operations))
	for _, op := range cap.Operations {
		if seen[op] {
			return fmt.Errorf("duplicate operation %q in capability %q", op, cap.ID)
		}
		seen[op] = true
	}
	c.caps = append(c.caps, cap)
	return nil
}
func (c *customBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	return nil, ErrExecutionNotAvailable
}
