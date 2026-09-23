package body

import (
	"testing"
)

func TestBodyID_LocalIsDeterministic(t *testing.T) {
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
}

func TestCapability_HasIDAndOperations(t *testing.T) {
	cap := Capability{
		ID:         "file:read",
		Operations: []string{"read"},
		Available:  true,
	}
	if cap.ID != "file:read" {
		t.Errorf("Capability.ID = %q, want %q", cap.ID, "file:read")
	}
	if len(cap.Operations) != 1 || cap.Operations[0] != "read" {
		t.Errorf("Capability.Operations = %v, want [read]", cap.Operations)
	}
	if !cap.Available {
		t.Error("Capability.Available = false, want true")
	}
}

func TestExecutionRequest_HasFields(t *testing.T) {
	req := ExecutionRequest{
		ExecutionID: "exec-1",
		Doll:        "neko-chan",
		Body:        LocalBodyID,
		Capability:  "runtime.info",
		Operation:   "read",
		Arguments:   map[string]any{"path": "/etc/hostname"},
	}
	if req.ExecutionID != "exec-1" {
		t.Errorf("ExecutionRequest.ExecutionID = %q, want %q", req.ExecutionID, "exec-1")
	}
	if req.Doll != "neko-chan" {
		t.Errorf("ExecutionRequest.Doll = %q, want %q", req.Doll, "neko-chan")
	}
	if req.Body != LocalBodyID {
		t.Errorf("ExecutionRequest.Body = %q, want %q", req.Body, LocalBodyID)
	}
	if req.Capability != "runtime.info" {
		t.Errorf("ExecutionRequest.Capability = %q, want %q", req.Capability, "runtime.info")
	}
	if req.Operation != "read" {
		t.Errorf("ExecutionRequest.Operation = %q, want %q", req.Operation, "read")
	}
	if req.Arguments["path"] != "/etc/hostname" {
		t.Errorf("ExecutionRequest.Arguments[\"path\"] = %v, want %v", req.Arguments["path"], "/etc/hostname")
	}
}

func TestExecutionStatus_Values(t *testing.T) {
	statuses := []ExecutionStatus{
		StatusPending, StatusRunning,
		StatusSuccess, StatusDenied,
		StatusUnsupported, StatusInvalid,
		StatusUnavailable, StatusFailed,
		StatusCompleted,
	}
	if len(statuses) != 9 {
		t.Fatalf("expected 9 statuses, got %d", len(statuses))
	}
	if StatusPending != "pending" {
		t.Errorf("StatusPending = %q", StatusPending)
	}
	if StatusRunning != "running" {
		t.Errorf("StatusRunning = %q", StatusRunning)
	}
	if StatusSuccess != "success" {
		t.Errorf("StatusSuccess = %q", StatusSuccess)
	}
	if StatusDenied != "denied" {
		t.Errorf("StatusDenied = %q", StatusDenied)
	}
	if StatusUnsupported != "unsupported" {
		t.Errorf("StatusUnsupported = %q", StatusUnsupported)
	}
	if StatusInvalid != "invalid" {
		t.Errorf("StatusInvalid = %q", StatusInvalid)
	}
	if StatusUnavailable != "unavailable" {
		t.Errorf("StatusUnavailable = %q", StatusUnavailable)
	}
	if StatusFailed != "failed" {
		t.Errorf("StatusFailed = %q", StatusFailed)
	}
	if StatusCompleted != "success" {
		t.Errorf("StatusCompleted = %q, want legacy alias for success", StatusCompleted)
	}
}

func TestExecutionResult_String(t *testing.T) {
	r1 := ExecutionResult{Status: StatusSuccess, Output: "hello"}
	if s := r1.String(); s != "success: hello" {
		t.Errorf("String() = %q, want %q", s, "success: hello")
	}

	r2 := ExecutionResult{Status: StatusFailed, Error: "permission denied"}
	if s := r2.String(); s != "failed: permission denied" {
		t.Errorf("String() = %q, want %q", s, "failed: permission denied")
	}

	r3 := ExecutionResult{Status: StatusDenied, ErrorCode: "no_rule", Error: "no matching rule"}
	if s := r3.String(); s != "denied: no matching rule" {
		t.Errorf("String() = %q, want %q", s, "denied: no matching rule")
	}
}

func TestExecutionResult_CarriesExecutionID(t *testing.T) {
	r := ExecutionResult{ExecutionID: "exec-1", Status: StatusSuccess, Output: "done"}
	if r.ExecutionID != "exec-1" {
		t.Errorf("ExecutionResult.ExecutionID = %q, want %q", r.ExecutionID, "exec-1")
	}
}

func TestExecutionResult_HasErrorCode(t *testing.T) {
	r := ExecutionResult{ExecutionID: "e", Status: StatusFailed, ErrorCode: "invocation_failed"}
	if r.ErrorCode != "invocation_failed" {
		t.Errorf("ExecutionResult.ErrorCode = %q, want %q", r.ErrorCode, "invocation_failed")
	}
}

func TestBody_IsAnInterface(t *testing.T) {
	var _ Body = (*LocalBody)(nil)
}
