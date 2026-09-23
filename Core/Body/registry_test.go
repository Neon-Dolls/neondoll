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
	r.Register(testBody)

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

func TestNewRegistry_LocalBodyHasNoCapabilities(t *testing.T) {
	r := NewRegistry()
	b, _ := r.Local()
	if len(b.Describe()) != 0 {
		t.Errorf("M1 LocalBody should have no capabilities, got %d", len(b.Describe()))
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

// NewCustomBody creates a simple Body for testing that satisfies the Body interface.
func NewCustomBody(id BodyID, kind BodyKind, name string) Body {
	return &customBody{id: id, kind: kind, name: name}
}

type customBody struct {
	id   BodyID
	kind BodyKind
	name string
}

func (c *customBody) ID() BodyID             { return c.id }
func (c *customBody) Kind() BodyKind         { return c.kind }
func (c *customBody) Name() string           { return c.name }
func (c *customBody) Describe() []Capability { return nil }
func (c *customBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	return nil, ErrExecutionNotAvailable
}