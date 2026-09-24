package body

import (
	"encoding/json"
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

func TestLocalBody_DescribeReturnsCapabilities(t *testing.T) {
	b := NewLocal()
	caps := b.Describe()
	if len(caps) != 1 {
		t.Fatalf("LocalBody.Describe() returned %d capabilities, want 1", len(caps))
	}
	if caps[0].ID != "runtime.info" {
		t.Errorf("capability ID = %q, want %q", caps[0].ID, "runtime.info")
	}
	if len(caps[0].Operations) != 2 || caps[0].Operations[0] != "read" || caps[0].Operations[1] != "list" {
		t.Errorf("capability Operations = %v, want [read list]", caps[0].Operations)
	}
	if !caps[0].Available {
		t.Error("capability Available = false, want true")
	}
}

func TestLocalBody_ResolveCapabilitySupportedAndAvailable(t *testing.T) {
	b := NewLocal()
	err := b.ResolveCapability("runtime.info", "read")
	if err != nil {
		t.Errorf("ResolveCapability(runtime.info, read) = %v, want nil", err)
	}
}

func TestLocalBody_ResolveCapabilityUnknownCapability(t *testing.T) {
	b := NewLocal()
	err := b.ResolveCapability("nonexistent", "read")
	if err != ErrUnsupportedCapability {
		t.Errorf("ResolveCapability(nonexistent, read) = %v, want ErrUnsupportedCapability", err)
	}
}

func TestLocalBody_ResolveCapabilityUnknownOperation(t *testing.T) {
	b := NewLocal()
	err := b.ResolveCapability("runtime.info", "write")
	if err != ErrUnsupportedOperation {
		t.Errorf("ResolveCapability(runtime.info, write) = %v, want ErrUnsupportedOperation", err)
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

func TestLocalBody_ExecuteRuntimeInfoRead(t *testing.T) {
	b := NewLocal()
	res, err := b.Execute(ExecutionRequest{Capability: "runtime.info", Operation: "read"})
	if err != nil {
		t.Fatalf("Execute(runtime.info, read) = %v, want nil", err)
	}
	if res == nil {
		t.Fatal("Execute(runtime.info, read) returned nil result")
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", res.Status, StatusSuccess)
	}
	if res.Output == "" {
		t.Error("Output must not be empty")
	}

	// AC13: structured payload with benign runtime facts only.
	var info RuntimeInfo
	if err := json.Unmarshal([]byte(res.Output), &info); err != nil {
		t.Fatalf("Output must be valid JSON: %v", err)
	}
	if info.OS == "" {
		t.Error("RuntimeInfo.OS must not be empty")
	}
	if info.Architecture == "" {
		t.Error("RuntimeInfo.Architecture must not be empty")
	}
	if info.GoVersion == "" {
		t.Error("RuntimeInfo.GoVersion must not be empty")
	}
}

func TestLocalBody_ExecuteRuntimeInfoRead_NoSensitiveAmbientData(t *testing.T) {
	b := NewLocal()
	res, err := b.Execute(ExecutionRequest{Capability: "runtime.info", Operation: "read"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res == nil || res.Output == "" {
		t.Fatal("no output")
	}

	// Unmarshal into a flexible map to check keys.
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Output), &raw); err != nil {
		t.Fatalf("Output must be valid JSON: %v", err)
	}

	// Only the three benign keys.
	expected := 3
	if len(raw) != expected {
		t.Errorf("expected %d fields in runtime info, got %d: %v", expected, len(raw), raw)
	}
	for k := range raw {
		switch k {
		case "os", "architecture", "go_version":
			// allowed
		default:
			t.Errorf("unexpected key in runtime info: %q", k)
		}
	}

	// Values must be strings.
	for k, v := range raw {
		if _, ok := v.(string); !ok {
			t.Errorf("runtime field %q is %T, want string", k, v)
		}
	}
}

func TestLocalBody_ExecuteUnknownOperationNotAvailable(t *testing.T) {
	b := NewLocal()
	_, err := b.Execute(ExecutionRequest{Capability: "runtime.info", Operation: "write"})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
	if !errors.Is(err, ErrExecutionNotAvailable) {
		t.Errorf("error = %v, want ErrExecutionNotAvailable", err)
	}
}

func TestLocalBody_ImplementsBodyInterface(t *testing.T) {
	var b Body = NewLocal()
	_ = b.ID()
	_ = b.Kind()
	_ = b.Name()
	_ = b.Describe()
	_ = b.ResolveCapability("runtime.info", "read")
	_ = b.RegisterCapability(Capability{})
}

func TestLocalBody_IdentityNotTransport(t *testing.T) {
	id := string(NewLocal().ID())
	transportTerms := []string{"ws", "socket", "tcp", "session", "connection", "transport"}
	for _, term := range transportTerms {
		if strings.Contains(strings.ToLower(id), term) {
			t.Errorf("LocalBody.ID() contains transport term %q: %q", term, id)
		}
	}
}

func TestLocalBody_NewLocalIsDeterministic(t *testing.T) {
	b1 := NewLocal()
	b2 := NewLocal()
	if b1.ID() != b2.ID() {
		t.Errorf("expected deterministic IDs, got %q and %q", b1.ID(), b2.ID())
	}
}

func TestLocalBody_ExecuteRuntimeInfoList(t *testing.T) {
	b := NewLocal()
	res, err := b.Execute(ExecutionRequest{Capability: "runtime.info", Operation: "list"})
	if err != nil {
		t.Fatalf("Execute(runtime.info, list) = %v, want nil", err)
	}
	if res == nil {
		t.Fatal("Execute(runtime.info, list) returned nil result")
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", res.Status, StatusSuccess)
	}
	if res.Output == "" {
		t.Error("Output must not be empty")
	}
}
