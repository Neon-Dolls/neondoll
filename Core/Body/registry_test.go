package body

import (
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
	if caps[0].Name != "runtime.info / read" {
		t.Errorf("capability Name = %q, want %q", caps[0].Name, "runtime.info / read")
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
	if caps[0].Name != "runtime.info / read" {
		t.Errorf("capability Name = %q, want %q", caps[0].Name, "runtime.info / read")
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
	custCap := Capability{Name: "custom:test", Description: "a test"}
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
	if caps[0].Name != "custom:test" {
		t.Errorf("capability Name = %q, want %q", caps[0].Name, "custom:test")
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

func (c *customBody) ID() BodyID             { return c.id }
func (c *customBody) Kind() BodyKind         { return c.kind }
func (c *customBody) Name() string           { return c.name }
func (c *customBody) Describe() []Capability { return c.caps }
func (c *customBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	return nil, ErrExecutionNotAvailable
}
