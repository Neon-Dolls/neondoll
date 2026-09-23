package body

import (
	"testing"
)

func TestNewCapabilityRegistry_Empty(t *testing.T) {
	cr := NewCapabilityRegistry()
	if cr == nil {
		t.Fatal("NewCapabilityRegistry returned nil")
	}
	caps := cr.All()
	if len(caps) != 0 {
		t.Errorf("expected 0 capabilities, got %d", len(caps))
	}
	if cr.Count() != 0 {
		t.Errorf("expected 0 count, got %d", cr.Count())
	}
}

func TestCapabilityRegistry_RegisterAndGet(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register("test:nop", "A test capability", map[string]string{})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	cap, ok := cr.Get("test:nop")
	if !ok {
		t.Fatal("expected to find registered capability")
	}
	if cap.Name != "test:nop" {
		t.Errorf("Name = %q, want %q", cap.Name, "test:nop")
	}
	if cap.Description != "A test capability" {
		t.Errorf("Description = %q, want %q", cap.Description, "A test capability")
	}
}

func TestCapabilityRegistry_RegisterWithParameters(t *testing.T) {
	cr := NewCapabilityRegistry()
	params := map[string]string{"path": "string", "mode": "string"}
	err := cr.Register("file:read", "Read files", params)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	cap, ok := cr.Get("file:read")
	if !ok {
		t.Fatal("expected to find capability")
	}
	if cap.Parameters["path"] != "string" {
		t.Errorf("Parameters[\"path\"] = %q, want %q", cap.Parameters["path"], "string")
	}
	if cap.Parameters["mode"] != "string" {
		t.Errorf("Parameters[\"mode\"] = %q, want %q", cap.Parameters["mode"], "string")
	}
}

func TestCapabilityRegistry_RegisterRejectsDuplicate(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register("test:nop", "First description", nil)
	if err != nil {
		t.Fatalf("first Register failed: %v", err)
	}
	err = cr.Register("test:nop", "Second description", nil)
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}
	// First registration must remain intact.
	cap, ok := cr.Get("test:nop")
	if !ok {
		t.Fatal("original capability must remain accessible")
	}
	if cap.Description != "First description" {
		t.Errorf("original capability Description = %q, want %q", cap.Description, "First description")
	}
}

func TestCapabilityRegistry_GetMissingReturnsFalse(t *testing.T) {
	cr := NewCapabilityRegistry()
	_, ok := cr.Get("nonexistent")
	if ok {
		t.Fatal("expected false for missing capability")
	}
}

func TestCapabilityRegistry_AllCount(t *testing.T) {
	cr := NewCapabilityRegistry()
	cr.Register("a:1", "one", nil)
	cr.Register("b:2", "two", nil)
	cr.Register("c:3", "three", nil)
	caps := cr.All()
	if len(caps) != 3 {
		t.Errorf("expected 3 capabilities, got %d", len(caps))
	}
}

func TestCapabilityRegistry_CountReflectsRegistrations(t *testing.T) {
	cr := NewCapabilityRegistry()
	if cr.Count() != 0 {
		t.Fatalf("expected 0, got %d", cr.Count())
	}
	cr.Register("test:1", "one", nil)
	if cr.Count() != 1 {
		t.Errorf("expected 1, got %d", cr.Count())
	}
}

func TestCapabilityRegistry_AllReturnsCopy(t *testing.T) {
	// Registering after All() must not affect the previously returned slice.
	cr := NewCapabilityRegistry()
	cr.Register("test:1", "one", nil)
	caps := cr.All()
	cr.Register("test:2", "two", nil)
	if len(caps) != 1 {
		t.Errorf("previously returned slice length = %d, want 1 (must be independent)", len(caps))
	}
}
