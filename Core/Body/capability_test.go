package body

import (
	"testing"
)

func TestNewCapabilityRegistry_Empty(t *testing.T) {
	cr := NewCapabilityRegistry()
	if cr.Count() != 0 {
		t.Errorf("Count = %d, want 0", cr.Count())
	}
	if caps := cr.All(); caps != nil {
		t.Errorf("All() = %v, want nil", caps)
	}
}

func TestCapabilityRegistry_RegisterAndRetrieve(t *testing.T) {
	cr := NewCapabilityRegistry()

	err := cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Available:  true,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if !cr.Has("runtime.info") {
		t.Error("Has(runtime.info) = false, want true")
	}
	if cr.Has("nonexistent") {
		t.Error("Has(nonexistent) = true, want false")
	}

	if cr.Count() != 1 {
		t.Errorf("Count = %d, want 1", cr.Count())
	}

	got, ok := cr.Get("runtime.info")
	if !ok {
		t.Fatal("Get(runtime.info) ok = false, want true")
	}
	if got.ID != "runtime.info" {
		t.Errorf("Get().ID = %q, want %q", got.ID, "runtime.info")
	}
	if len(got.Operations) != 1 || got.Operations[0] != "read" {
		t.Errorf("Get().Operations = %v, want [read]", got.Operations)
	}
	if !got.Available {
		t.Error("Get().Available = false, want true")
	}
}

func TestCapabilityRegistry_InvariantEmptyID(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register(Capability{
		ID:         "",
		Operations: []string{"read"},
	})
	if err == nil {
		t.Fatal("Register(empty ID) = nil, want error")
	}
	if cr.Count() != 0 {
		t.Errorf("Count = %d, want 0 (registry must not be modified on error)", cr.Count())
	}
}

func TestCapabilityRegistry_InvariantNoOperations(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register(Capability{
		ID: "runtime.info",
	})
	if err == nil {
		t.Fatal("Register(no operations) = nil, want error")
	}
	if cr.Count() != 0 {
		t.Errorf("Count = %d, want 0 (registry must not be modified on error)", cr.Count())
	}
}

func TestCapabilityRegistry_InvariantDuplicateID(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
	})
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	err = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"write"},
	})
	if err == nil {
		t.Fatal("Register(duplicate ID) = nil, want error")
	}
}

func TestCapabilityRegistry_InvariantDuplicateOperation(t *testing.T) {
	cr := NewCapabilityRegistry()
	err := cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read", "read"},
	})
	if err == nil {
		t.Fatal("Register(duplicate operation) = nil, want error")
	}
	if cr.Count() != 0 {
		t.Errorf("Count = %d, want 0 (registry must not be modified on error)", cr.Count())
	}
}

func TestCapabilityRegistry_ResolveSupportedAndAvailable(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Available:  true,
	})

	err := cr.Resolve("runtime.info", "read")
	if err != nil {
		t.Errorf("Resolve(runtime.info, read) = %v, want nil", err)
	}
}

func TestCapabilityRegistry_ResolveUnknownCapability(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
	})

	err := cr.Resolve("nonexistent", "read")
	if err != ErrUnsupportedCapability {
		t.Errorf("Resolve(nonexistent, read) = %v, want ErrUnsupportedCapability", err)
	}
}

func TestCapabilityRegistry_ResolveUnknownOperation(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
	})

	err := cr.Resolve("runtime.info", "write")
	if err != ErrUnsupportedOperation {
		t.Errorf("Resolve(runtime.info, write) = %v, want ErrUnsupportedOperation", err)
	}
}

func TestCapabilityRegistry_ResolveUnavailable(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Available:  false,
	})

	err := cr.Resolve("runtime.info", "read")
	if err != ErrUnavailable {
		t.Errorf("Resolve(runtime.info, read) = %v, want ErrUnavailable", err)
	}
}

func TestCapabilityRegistry_AllReturnsCopies(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
	})

	// Modify the returned copy — original must not change.
	all := cr.All()
	if len(all) == 0 {
		t.Fatal("All() returned empty")
	}
	all[0].Operations[0] = "hacked"

	original, _ := cr.Get("runtime.info")
	if original.Operations[0] == "hacked" {
		t.Error("Get() returned a shared reference, not a copy")
	}
}

func TestCapabilityRegistry_GetReturnsCopy(t *testing.T) {
	cr := NewCapabilityRegistry()
	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Constraints: map[string]string{
			"key": "value",
		},
	})

	got1, _ := cr.Get("runtime.info")
	got1.Constraints["key"] = "hacked"

	got2, _ := cr.Get("runtime.info")
	if got2.Constraints["key"] == "hacked" {
		t.Error("Get() returned a shared reference for Constraints, not a copy")
	}
}

func TestCapabilityRegistry_MultipleCapabilities(t *testing.T) {
	cr := NewCapabilityRegistry()

	_ = cr.Register(Capability{
		ID:         "runtime.info",
		Operations: []string{"read"},
		Available:  true,
	})
	_ = cr.Register(Capability{
		ID:         "file.access",
		Operations: []string{"read", "write"},
		Available:  false,
	})

	if cr.Count() != 2 {
		t.Errorf("Count = %d, want 2", cr.Count())
	}

	// Resolve first cap.
	if err := cr.Resolve("runtime.info", "read"); err != nil {
		t.Errorf("runtime.info/read: %v, want nil", err)
	}

	// Resolve second cap — unavailable.
	if err := cr.Resolve("file.access", "read"); err != ErrUnavailable {
		t.Errorf("file.access/read: %v, want ErrUnavailable", err)
	}

	// Resolve unsupported operation on second cap.
	if err := cr.Resolve("file.access", "delete"); err != ErrUnsupportedOperation {
		t.Errorf("file.access/delete: %v, want ErrUnsupportedOperation", err)
	}
}
