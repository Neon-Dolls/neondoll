//go:build e2e

package integration

import (
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// TestM2LocalBodyHasCapabilities proves Milestone 2 "Local Body Has
// Capabilities" end-to-end acceptance:
//
//	Core starts
//	    ↓
//	Local Body exists (M1 invariant)
//	    ↓
//	Local Body advertises at least one capability via Describe()
//	    ↓
//	The capability has Name "runtime.info / read"
//	    ↓
//	Registry.GetCapabilities() resolves capabilities by BodyID
//	    ↓
//	Registry.AllCapabilities() enumerates all Bodies' capabilities
func TestM2LocalBodyHasCapabilities(t *testing.T) {
	// ── Simulate Core startup ──
	reg := body.NewRegistry()

	// ── AC1: Local Body exists (M1 invariant) ──
	localBody, ok := reg.Local()
	if !ok {
		t.Fatal("AC1: Local Body not found in registry")
	}

	// ── AC2: Local Body advertises capabilities via Describe() ──
	caps := localBody.Describe()
	if len(caps) == 0 {
		t.Fatal("AC2: LocalBody should have at least 1 capability in M2")
	}

	// ── AC3: The capability has Name "runtime.info / read" ──
	found := false
	for _, c := range caps {
		if c.Name == "runtime.info / read" {
			found = true
			if c.Description == "" {
				t.Error("AC3: runtime.info / read must have a non-empty Description")
			}
			break
		}
	}
	if !found {
		t.Fatal("AC3: Local Body must advertise the runtime.info / read capability")
	}

	// ── AC4: Registry.GetCapabilities() resolves capabilities for Local Body ──
	regCaps, err := reg.GetCapabilities(body.LocalBodyID)
	if err != nil {
		t.Fatalf("AC4: GetCapabilities(LocalBodyID) failed: %v", err)
	}
	if len(regCaps) == 0 {
		t.Fatal("AC4: GetCapabilities(LocalBodyID) returned empty")
	}
	if regCaps[0].Name != "runtime.info / read" {
		t.Errorf("AC4: capability Name = %q, want %q", regCaps[0].Name, "runtime.info / read")
	}

	// ── AC5: Registry.AllCapabilities() enumerates by BodyID ──
	all := reg.AllCapabilities()
	if len(all) != 1 {
		t.Fatalf("AC5: expected 1 Body in AllCapabilities, got %d", len(all))
	}
	localCaps, ok := all[body.LocalBodyID]
	if !ok {
		t.Fatal("AC5: AllCapabilities must include LocalBodyID key")
	}
	if len(localCaps) < 1 {
		t.Fatal("AC5: Local Body must have at least 1 capability via AllCapabilities")
	}
}
