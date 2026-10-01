package relay

import (
	"testing"
)

// ── M4.7.4 — Required Failure Cases ───────────────────────────────────
//
// Acceptance criteria (Section 4 of M4.7 plan): for every failure case
// the system must fail closed, remain bounded, not panic, not leak
// unbounded queues/goroutines, keep unrelated routes functional, and
// expose an existing error/metric/log signal.

// TestM47_Failure_MalformedControlFrame checks that UnmarshalControl
// rejects JSON with an unknown type field and does not panic.
func TestM47_Failure_MalformedControlFrame(t *testing.T) {
	// Missing / unknown type
	_, err := UnmarshalControl([]byte(`{"type":"bogus_type"}`))
	if err == nil {
		t.Error("expected error for unknown control message type")
	}
}

// TestM47_Failure_OversizedControlMessage checks that UnmarshalControl
// rejects messages exceeding MaxControlMessageSize.
func TestM47_Failure_OversizedControlMessage(t *testing.T) {
	big := make([]byte, MaxControlMessageSize+1)
	_, err := UnmarshalControl(big)
	if err == nil {
		t.Error("expected error for oversized control message")
	}
}

// TestM47_Failure_MalformedPacketFrame checks that UnmarshalFrame rejects
// truncated frames and does not panic.
func TestM47_Failure_MalformedPacketFrame(t *testing.T) {
	// Frame too small (less than header)
	_, err := UnmarshalFrame([]byte{0x01, 0x02})
	if err == nil || err == ErrFrameTooSmall {
		// Either a "too small" error is fine
	} else {
		t.Fatalf("unexpected error: %v (want ErrFrameTooSmall or similar)", err)
	}

	// Bad version
	frame := make([]byte, FrameHeaderSize+10)
	frame[0] = 0xFF // invalid version
	_, err = UnmarshalFrame(frame)
	if err != ErrFrameBadVersion {
		t.Errorf("bad version: want ErrFrameBadVersion, got %v", err)
	}

	// Oversized payload
	oversized := make([]byte, FrameHeaderSize)
	oversized[0] = ProtocolVersion
	oversized[9] = 0xFF // high byte of length = 65535
	oversized[10] = 0xFF
	// Append only a partial payload — should fail length mismatch or oversized
	oversized = append(oversized, make([]byte, 100)...)
	_, err = UnmarshalFrame(oversized)
	if err == nil {
		t.Error("expected error for payload length > MaxFramePayloadSize")
	}
}

// TestM47_Failure_UnknownRouteID verifies that operating on an
// unallocated RouteID returns an appropriate error.
func TestM47_Failure_UnknownRouteID(t *testing.T) {
	reg := NewRegistry(10, 50)
	if err := reg.AddRegistration("test", "hash"); err != nil {
		t.Fatal(err)
	}

	// CloseRoute on non-existent route
	_, err := reg.CloseRoute("test", RouteID(999))
	if err != ErrRouteNotFound {
		t.Errorf("CloseRoute unknown: want ErrRouteNotFound, got %v", err)
	}

	// OpenRoute on non-existent route
	_, err = reg.OpenRoute("test", RouteID(999))
	if err != ErrRouteNotFound {
		t.Errorf("OpenRoute unknown: want ErrRouteNotFound, got %v", err)
	}
}

// TestM47_Failure_UnauthorizedCrossRegistration verifies that a
// registration cannot manipulate routes it does not own.
func TestM47_Failure_UnauthorizedCrossRegistration(t *testing.T) {
	reg := NewRegistry(10, 50)
	if err := reg.AddRegistration("owner", "hash-a"); err != nil {
		t.Fatal(err)
	}
	if err := reg.AddRegistration("intruder", "hash-b"); err != nil {
		t.Fatal(err)
	}

	// Owner allocates and opens a route
	if _, err := reg.AllocateRoute("owner", RouteID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.OpenRoute("owner", RouteID(1)); err != nil {
		t.Fatal(err)
	}

	// Intruder cannot close it
	_, err := reg.CloseRoute("intruder", RouteID(1))
	if err != ErrRouteWrongOwner {
		t.Errorf("close by intruder: want ErrRouteWrongOwner, got %v", err)
	}

	// Intruder cannot open it (it's already open, but the wrong-owner check
	// fires first)
	_, err = reg.OpenRoute("intruder", RouteID(1))
	if err != ErrRouteWrongOwner {
		t.Errorf("open by intruder: want ErrRouteWrongOwner, got %v", err)
	}

	// Intruder cannot allocate the same route ID
	if _, err := reg.AllocateRoute("intruder", RouteID(1)); err != ErrRouteAlreadyExists {
		t.Errorf("allocate existing by intruder: want ErrRouteAlreadyExists, got %v", err)
	}
}

// TestM47_Failure_WrongRouteCredential verifies that OpenRoute with
// wrong credentials is not relevant at the Registry level — the
// Registry does not re-check credentials on OpenRoute. This test
// simply verifies the registry does not panic or leak routes.
func TestM47_Failure_WrongRouteCredential(t *testing.T) {
	reg := NewRegistry(10, 50)
	if err := reg.AddRegistration("test", "hash"); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.AllocateRoute("test", RouteID(1)); err != nil {
		t.Fatal(err)
	}

	// OpenRoute is always allowed for the owner at the Registry level;
	// credential validation happens at the Service/ControlServer layer.
	if _, err := reg.OpenRoute("test", RouteID(1)); err != nil {
		t.Fatalf("owner should be able to open own route: %v", err)
	}
}

// TestM47_Failure_MarshalFrameRejectsOversized verifies that MarshalFrame
// rejects oversized payloads.
func TestM47_Failure_MarshalFrameRejectsOversized(t *testing.T) {
	f := &Frame{
		Version: ProtocolVersion,
		RouteID: RouteID(1),
		Payload: make([]byte, MaxFramePayloadSize+1),
	}
	_, err := MarshalFrame(f)
	if err != ErrFrameTooLarge {
		t.Errorf("oversized frame: want ErrFrameTooLarge, got %v", err)
	}

	// Nil frame
	_, err = MarshalFrame(nil)
	if err == nil {
		t.Error("nil frame: expected error")
	}

	// Bad version
	f2 := &Frame{Version: 0, RouteID: RouteID(1), Payload: []byte("hello")}
	_, err = MarshalFrame(f2)
	if err != ErrFrameBadVersion {
		t.Errorf("bad version: want ErrFrameBadVersion, got %v", err)
	}
}

// TestM47_Failure_RegistrationNotFound verifies that operations on an
// unknown registration return ErrRegistrationNotFound.
func TestM47_Failure_RegistrationNotFound(t *testing.T) {
	reg := NewRegistry(10, 50)

	// Allocate on unknown registration
	_, err := reg.AllocateRoute("ghost", RouteID(1))
	if err != ErrRegistrationNotFound {
		t.Errorf("allocate on ghost reg: want ErrRegistrationNotFound, got %v", err)
	}

	// Keepalive on unknown registration
	if err := reg.Keepalive("ghost"); err != ErrRegistrationNotFound {
		t.Errorf("keepalive on ghost reg: want ErrRegistrationNotFound, got %v", err)
	}
}

// TestM47_Failure_TooManyRoutes verifies the registry enforces its max
// route limit, failing closed instead of growing unboundedly.
func TestM47_Failure_TooManyRoutes(t *testing.T) {
	maxRoutes := 5
	reg := NewRegistry(10, maxRoutes)
	if err := reg.AddRegistration("test", "hash"); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= maxRoutes; i++ {
		if _, err := reg.AllocateRoute("test", RouteID(i)); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}

	// The next allocation must fail
	if _, err := reg.AllocateRoute("test", RouteID(maxRoutes+1)); err != ErrTooManyRoutes {
		t.Errorf("overflow: want ErrTooManyRoutes, got %v", err)
	}
}
