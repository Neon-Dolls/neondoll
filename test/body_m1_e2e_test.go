//go:build e2e

package integration

import (
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// TestM1LocalBodyExists proves Milestone 1 "Local Body Exists" acceptance:
//
//	Core starts
//	    ↓
//	Local Body exists
//	    ↓
//	Core can identify it through the Body boundary
//	    ↓
//	Core stops / starts normally
//
// Tests prove exactly one mandatory Local Body and identity not confused
// with transport connection or Interaction Session.
func TestM1LocalBodyExists(t *testing.T) {
	// ── Simulate Core startup: create the Body boundary ──
	registry := body.NewRegistry()

	// ── AC1: Core starts → Local Body exists ──
	if registry.Count() != 1 {
		t.Fatalf("AC1: expected exactly 1 Body in registry, got %d", registry.Count())
	}

	localBody, ok := registry.Local()
	if !ok {
		t.Fatal("AC1: Local Body not found in registry")
	}
	if localBody == nil {
		t.Fatal("AC1: Local Body is nil")
	}

	// ── AC2: Core can identify it through the Body boundary ──
	if localBody.ID() != body.LocalBodyID {
		t.Errorf("AC2: LocalBody.ID() = %q, want %q", localBody.ID(), body.LocalBodyID)
	}
	if localBody.Kind() != body.BodyKindLocal {
		t.Errorf("AC2: LocalBody.Kind() = %q, want %q", localBody.Kind(), body.BodyKindLocal)
	}
	if localBody.Name() == "" {
		t.Error("AC2: LocalBody.Name() must not be empty")
	}

	// ── AC3: Identity not confused with transport or session ──
	id := string(localBody.ID())
	transportTerms := []string{"ws", "socket", "tcp", "session", "connection", "transport"}
	for _, term := range transportTerms {
		if strings.Contains(strings.ToLower(id), term) {
			t.Errorf("AC3: LocalBody.ID() contains transport term %q: %q", term, id)
		}
	}

	// ── AC4: Exactly one Body in registry (no remote/network Bodies) ──
	if registry.Count() != 1 {
		t.Errorf("AC4: expected exactly 1 Body, got %d — remote Bodies must not exist in M1", registry.Count())
	}

	// ── AC5: Local Body advertises capabilities in M2 ──
	caps := localBody.Describe()
	if len(caps) != 1 {
		t.Fatalf("AC5: M2 LocalBody should have 1 capability, got %d", len(caps))
	}
	if caps[0].ID != "runtime.info" {
		t.Errorf("AC5: capability ID = %q, want %q", caps[0].ID, "runtime.info")
	}
	if len(caps[0].Operations) != 1 || caps[0].Operations[0] != "read" {
		t.Errorf("AC5: capability Operations = %v, want [read]", caps[0].Operations)
	}
	if !caps[0].Available {
		t.Error("AC5: capability Available = false, want true")
	}

	// ── AC6: Core stops / starts normally (second Registry creation) ──
	registry2 := body.NewRegistry()
	if registry2.Count() != 1 {
		t.Fatalf("AC6: new Registry must have 1 Body, got %d", registry2.Count())
	}
	b2, ok2 := registry2.Local()
	if !ok2 || b2.ID() != body.LocalBodyID {
		t.Fatal("AC6: Local Body must exist after re-creation")
	}
}

// TestM1LocalBodyIsNotAnInteractionSession proves the Body boundary is not
// confused with the Interaction Service or any session concept.
func TestM1LocalBodyIsNotAnInteractionSession(t *testing.T) {
	registry := body.NewRegistry()
	localBody, _ := registry.Local()

	id := string(localBody.ID())

	// The ID must not reference session concepts.
	sessionTerms := []string{"session", "interaction", "chat", "conversation", "dialog"}
	for _, term := range sessionTerms {
		if strings.Contains(strings.ToLower(id), term) {
			t.Errorf("LocalBody.ID() contains session term %q: %q", term, id)
		}
	}
}

// TestM1RegistryIsDiscoverable proves the Body boundary is discoverable
// through the Registry's public interface, not through internal state.
func TestM1RegistryIsDiscoverable(t *testing.T) {
	registry := body.NewRegistry()

	// All() returns the bodies — this is the discovery API.
	all := registry.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 Body from All(), got %d", len(all))
	}

	// Get() by ID works.
	b, ok := registry.Get(body.LocalBodyID)
	if !ok {
		t.Fatal("Get(LocalBodyID) must succeed")
	}
	if b == nil {
		t.Fatal("Get(LocalBodyID) returned nil Body")
	}
}

// TestM1LocalBodyDoesNotRequireNetwork proves the Local Body requires no
// remote Body, Desktop Body, network, or external service.
func TestM1LocalBodyDoesNotRequireNetwork(t *testing.T) {
	// Creating a Registry must not panic, crash, or require network.
	// This test passes by construction — no network calls exist in the
	// body package.
	registry := body.NewRegistry()
	_, ok := registry.Local()
	if !ok {
		t.Fatal("Local Body must exist without network")
	}
}
