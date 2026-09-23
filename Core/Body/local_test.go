package body

import (
	"errors"
	"strings"
	"testing"
)

func TestNewLocal_CreatesBody(t *testing.T) {
	b := NewLocal()
	if b == nil {
		t.Fatal("NewLocal returned nil")
	}
}

func TestLocalBody_HasDeterministicID(t *testing.T) {
	b := NewLocal()
	if b.ID() != LocalBodyID {
		t.Errorf("LocalBody.ID() = %q, want %q", b.ID(), LocalBodyID)
	}
}

func TestLocalBody_KindIsLocal(t *testing.T) {
	b := NewLocal()
	if b.Kind() != BodyKindLocal {
		t.Errorf("LocalBody.Kind() = %q, want %q", b.Kind(), BodyKindLocal)
	}
}

func TestLocalBody_HasName(t *testing.T) {
	b := NewLocal()
	if b.Name() == "" {
		t.Fatal("LocalBody.Name() must not be empty")
	}
}

func TestLocalBody_NameDefault(t *testing.T) {
	b := NewLocal()
	if b.Name() != "local" {
		t.Errorf("LocalBody.Name() = %q, want %q", b.Name(), "local")
	}
}

func TestLocalBody_DescribeNil(t *testing.T) {
	b := NewLocal()
	caps := b.Describe()
	if len(caps) != 0 {
		t.Errorf("LocalBody.Describe() = %v, want empty slice", caps)
	}
}

func TestLocalBody_ExecuteNotAvailable(t *testing.T) {
	b := NewLocal()
	_, err := b.Execute(ExecutionRequest{Capability: "test:nop"})
	if err == nil {
		t.Fatal("expected error from LocalBody.Execute")
	}
	if !errors.Is(err, ErrExecutionNotAvailable) {
		t.Errorf("error = %v, want ErrExecutionNotAvailable", err)
	}
}

func TestLocalBody_ImplementsBodyInterface(t *testing.T) {
	// Compile-time check: *LocalBody satisfies Body.
	var b Body = NewLocal()
	_ = b.ID()
	_ = b.Kind()
	_ = b.Name()
	_ = b.Describe()
}

func TestLocalBody_IdentityNotTransport(t *testing.T) {
	// The identity must not reference transport concepts like
	// websocket, ws, session, tcp, or connection.
	id := string(NewLocal().ID())
	transportTerms := []string{"ws", "socket", "tcp", "session", "connection", "transport"}
	for _, term := range transportTerms {
		if strings.Contains(strings.ToLower(id), term) {
			t.Errorf("LocalBody.ID() contains transport term %q: %q", term, id)
		}
	}
}

func TestLocalBody_NewLocalIsDeterministic(t *testing.T) {
	// Two Local Bodies created with defaults must have the same ID.
	b1 := NewLocal()
	b2 := NewLocal()
	if b1.ID() != b2.ID() {
		t.Errorf("expected deterministic IDs, got %q and %q", b1.ID(), b2.ID())
	}
}
