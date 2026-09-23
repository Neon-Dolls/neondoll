package body

import (
	"testing"
)

func TestBodyID_LocalIsDeterministic(t *testing.T) {
	// LocalBodyID must be a specific, non-empty constant.
	if LocalBodyID == "" {
		t.Fatal("LocalBodyID must not be empty")
	}
	if LocalBodyID != "local::core" {
		t.Fatalf("LocalBodyID = %q, want %q", LocalBodyID, "local::core")
	}
}

func TestBodyKind_Values(t *testing.T) {
	if BodyKindLocal != "local" {
		t.Fatalf("BodyKindLocal = %q, want %q", BodyKindLocal, "local")
	}
	if BodyKindRemote != "remote" {
		t.Fatalf("BodyKindRemote = %q, want %q", BodyKindRemote, "remote")
	}
}

func TestCapability_HasName(t *testing.T) {
	cap := Capability{
		Name:        "file:read",
		Description: "Read a file from the local filesystem",
		Parameters:  map[string]string{"path": "string"},
	}
	if cap.Name != "file:read" {
		t.Errorf("Capability.Name = %q, want %q", cap.Name, "file:read")
	}
}

func TestExecutionRequest_HasFields(t *testing.T) {
	req := ExecutionRequest{
		Capability: "file:read",
		Parameters: map[string]any{"path": "/etc/hostname"},
	}
	if req.Capability != "file:read" {
		t.Errorf("ExecutionRequest.Capability = %q, want %q", req.Capability, "file:read")
	}
	if req.Parameters["path"] != "/etc/hostname" {
		t.Errorf("ExecutionRequest.Parameters[\"path\"] = %v, want %v", req.Parameters["path"], "/etc/hostname")
	}
}

func TestExecutionStatus_Values(t *testing.T) {
	statuses := []ExecutionStatus{StatusPending, StatusRunning, StatusCompleted, StatusFailed, StatusDenied}
	expected := 5
	if len(statuses) != expected {
		t.Fatalf("expected %d statuses, got %d", expected, len(statuses))
	}
	if StatusPending != "pending" {
		t.Errorf("StatusPending = %q", StatusPending)
	}
	if StatusDenied != "denied" {
		t.Errorf("StatusDenied = %q", StatusDenied)
	}
}

func TestExecutionResult_String(t *testing.T) {
	r1 := ExecutionResult{Status: StatusCompleted, Output: "hello"}
	if s := r1.String(); s != "completed: hello" {
		t.Errorf("String() = %q, want %q", s, "completed: hello")
	}

	r2 := ExecutionResult{Status: StatusFailed, Error: "permission denied"}
	if s := r2.String(); s != "failed: permission denied" {
		t.Errorf("String() = %q, want %q", s, "failed: permission denied")
	}
}

func TestBody_IsAnInterface(t *testing.T) {
	// Compile check: Body must be an interface type.
	var _ Body = (*LocalBody)(nil)
}