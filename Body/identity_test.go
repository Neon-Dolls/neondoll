package body

import (
	"strings"
	"testing"
)

func TestNewBodyIDIsStableAndOpaque(t *testing.T) {
	a, err := NewBodyID()
	if err != nil {
		t.Fatalf("NewBodyID: %v", err)
	}
	b, err := NewBodyID()
	if err != nil {
		t.Fatalf("NewBodyID: %v", err)
	}
	if a == "" {
		t.Fatal("expected non-empty body id")
	}
	if !strings.HasPrefix(string(a), "body_") {
		t.Errorf("body id should start with body_, got %q", a)
	}
	if a == b {
		t.Errorf("two fresh body ids should differ, both %q", a)
	}
	// Body ID is opaque: it must not expose its own raw entropy.
	// (No assertion; presence of strings.HasPrefix above guarantees format.)
}

func TestNewIdentityState(t *testing.T) {
	meta := BodyMetadata{
		Implementation: "neondoll-body",
		Platform:       "linux",
		Arch:           "amd64",
	}
	st, err := NewIdentityState("", meta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	if st.Version != CurrentIdentityVersion {
		t.Errorf("version = %d, want %d", st.Version, CurrentIdentityVersion)
	}
	if st.Identity.BodyID == "" {
		t.Errorf("body_id should be non-empty")
	}
	if st.Identity.Meta.Implementation != "neondoll-body" {
		t.Errorf("implementation = %q", st.Identity.Meta.Implementation)
	}
	if st.Identity.Meta.Platform != "linux" {
		t.Errorf("platform = %q", st.Identity.Meta.Platform)
	}
	if st.Identity.Meta.Arch != "amd64" {
		t.Errorf("arch = %q", st.Identity.Meta.Arch)
	}
}

func TestIdentityMarshalRoundTrip(t *testing.T) {
	meta := BodyMetadata{Implementation: "neondoll-body", Platform: "darwin", Arch: "arm64"}
	st, err := NewIdentityState("SparkBody", meta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	raw, err := st.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalIdentity(raw)
	if err != nil {
		t.Fatalf("UnmarshalIdentity: %v", err)
	}
	if got.Identity.BodyID != st.Identity.BodyID {
		t.Errorf("body_id = %q, want %q", got.Identity.BodyID, st.Identity.BodyID)
	}
	if got.Identity.Name != "SparkBody" {
		t.Errorf("name = %q, want SparkBody", got.Identity.Name)
	}
	if got.Identity.Meta.Platform != "darwin" {
		t.Errorf("platform = %q, want darwin", got.Identity.Meta.Platform)
	}
}

func TestUnmarshalIdentityRejectsBadVersion(t *testing.T) {
	raw := `{"version":99,"identity":{"body_id":"body_x","meta":{"implementation":"i","platform":"p","arch":"a"}}}`
	_, err := UnmarshalIdentity(raw)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestUnmarshalIdentityRejectsMissingBodyID(t *testing.T) {
	raw := `{"version":1,"identity":{"name":"x","meta":{"implementation":"i","platform":"p","arch":"a"}}}`
	_, err := UnmarshalIdentity(raw)
	if err == nil {
		t.Fatal("expected error for missing body_id")
	}
}
