//go:build e2e

package integration

import (
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// TestM2LocalBodyHasCapabilities proves Milestone 2 "Local Body Has
// Capabilities" acceptance through the actual Local Body / Core Body
// boundary:
//
//	Core starts
//	    ↓
//	Body Registry exposes Local Body
//	    ↓
//	Core asks Local Body for capabilities
//	    ↓
//	runtime.info is advertised
//	    ↓
//	Core resolves runtime.info / read
//	    ↓
//	supported + available
//
// The capability model separates capability ID from operation, carries
// explicit availability, and resolution distinguishes unsupported
// capability, unsupported operation, and unavailable capability.
func TestM2LocalBodyHasCapabilities(t *testing.T) {
	// ── Simulate Core startup: create the Body boundary ──
	registry := body.NewRegistry()

	localBody, ok := registry.Local()
	if !ok {
		t.Fatal("Local Body not found in registry")
	}

	// ── AC1: Local Body exists through the Body boundary ──
	if localBody.ID() != body.LocalBodyID {
		t.Fatalf("AC1: LocalBody.ID() = %q, want %q", localBody.ID(), body.LocalBodyID)
	}

	// ── AC2: Core asks Local Body for capabilities ──
	caps := localBody.Describe()
	if len(caps) != 1 {
		t.Fatalf("AC2: LocalBody should expose 1 capability, got %d", len(caps))
	}

	// ── AC3: runtime.info is advertised with distinct operation ──
	cap := caps[0]
	if cap.ID != "runtime.info" {
		t.Errorf("AC3: capability ID = %q, want %q", cap.ID, "runtime.info")
	}
	if len(cap.Operations) != 1 || cap.Operations[0] != "read" {
		t.Errorf("AC3: capability Operations = %v, want [read]", cap.Operations)
	}
	if !cap.Available {
		t.Error("AC3: capability Available = false, want true")
	}

	// ── AC4: Core resolves (runtime.info, read) directly on the Local Body ──
	if err := localBody.ResolveCapability("runtime.info", "read"); err != nil {
		t.Errorf("AC4: resolve runtime.info/read = %v, want nil (supported + available)", err)
	}

	// ── AC5: Core resolves (runtime.info, read) through the Body Registry ──
	if err := registry.ResolveCapability(body.LocalBodyID, "runtime.info", "read"); err != nil {
		t.Errorf("AC5: registry resolve runtime.info/read = %v, want nil (supported + available)", err)
	}

	// ── AC6: unknown capability → explicit unsupported capability ──
	if err := registry.ResolveCapability(body.LocalBodyID, "no.such.capability", "read"); err != body.ErrUnsupportedCapability {
		t.Errorf("AC6: unexpected error for unknown capability: %v, want ErrUnsupportedCapability", err)
	}

	// ── AC7: known capability + unknown operation → explicit unsupported operation ──
	if err := registry.ResolveCapability(body.LocalBodyID, "runtime.info", "write"); err != body.ErrUnsupportedOperation {
		t.Errorf("AC7: unexpected error for unsupported operation: %v, want ErrUnsupportedOperation", err)
	}

	// ── AC8: known but unavailable capability → explicit unavailable ──
	// Declare an unavailable capability on the actual Local Body through
	// the Body boundary, then resolve it.
	blocked := body.Capability{
		ID:         "test:blocked",
		Operations: []string{"do-thing"},
		Available:  false,
	}
	if err := registry.RegisterCapability(body.LocalBodyID, blocked); err != nil {
		t.Fatalf("AC8: declare unavailable capability failed: %v", err)
	}
	if err := registry.ResolveCapability(body.LocalBodyID, "test:blocked", "do-thing"); err != body.ErrUnavailable {
		t.Errorf("AC8: unexpected error for unavailable capability: %v, want ErrUnavailable", err)
	}
}

// TestM2CapabilityModelNotExecutable proves M2 stays discovery-only:
// resolution exists, execution does not.
func TestM2CapabilityModelNotExecutable(t *testing.T) {
	registry := body.NewRegistry()
	localBody, _ := registry.Local()

	// Capability is discoverable and resolvable...
	if err := localBody.ResolveCapability("runtime.info", "read"); err != nil {
		t.Fatalf("runtime.info/read must resolve: %v", err)
	}

	// ...but execution is explicitly not available in M2.
	_, err := localBody.Execute(body.ExecutionRequest{Capability: "runtime.info"})
	if !errors.Is(err, body.ErrExecutionNotAvailable) {
		t.Errorf("Execute(runtime.info) = %v, want ErrExecutionNotAvailable", err)
	}
}
