package bodyruntime

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func meta_() BodyMetadata {
	return BodyMetadata{Implementation: "neondoll-body", Platform: "linux", Arch: "amd64"}
}

// TestFreshCreateMakesIdentityOnce: a fresh install creates identity once.
func TestFreshCreateMakesIdentityOnce(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	res, err := s.CreateFresh("SparkBody", meta_())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	if res.State.Identity.BodyID == "" {
		t.Error("body_id should be non-empty")
	}
	// Reloading must return the SAME identity — create did not double-generate.
	st, kp, err2 := s.LoadOrError()
	if err2 != nil {
		t.Fatalf("LoadOrError after create: %v", err2)
	}
	if st.Identity.BodyID != res.State.Identity.BodyID {
		t.Errorf("reload body_id = %q, want %q", st.Identity.BodyID, res.State.Identity.BodyID)
	}
	if kp.PublicKeyBase64() != res.Key.PublicKeyBase64() {
		t.Errorf("reload public key differs from created keypair")
	}
}

// TestRestartPreservesIdentityAndKey: a restart loads the same identity/key.
func TestRestartPreservesIdentityAndKey(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	res, err := s.CreateFresh("SparkBody", meta_())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	// Simulate a restart: a brand-new Store instance on the same directory.
	s2 := NewStore(dir)
	st, kp, err2 := s2.LoadOrError()
	if err2 != nil {
		t.Fatalf("LoadOrError after restart: %v", err2)
	}
	if st.Identity.BodyID != res.State.Identity.BodyID {
		t.Errorf("restart changed body_id: %q vs %q", st.Identity.BodyID, res.State.Identity.BodyID)
	}
	if st.Identity.Name != "SparkBody" {
		t.Errorf("restart changed name: %q", st.Identity.Name)
	}
	if kp.PublicKeyBase64() != res.Key.PublicKeyBase64() {
		t.Errorf("restart changed WG public key")
	}
}

// TestLoadBeforeCreateIsNotFound: loading with no state is a clean
// "not found", and does not silently create anything.
func TestLoadBeforeCreateIsNotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_, _, err := s.LoadOrError()
	if err != ErrStateNotFound {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
	if s.HasIdentity() {
		t.Error("HasIdentity() should be false before any create")
	}
}

// TestCorruptIdentityFailsSafe: corrupt state is surfaced as an error and is
// NOT silently replaced.
func TestCorruptIdentityFailsSafe(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	res, err := s.CreateFresh("SparkBody", meta_())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	// Corrupt the identity file.
	data := []byte(`{ "this is not valid identity json"`)
	if err := os.WriteFile(s.statePath(), data, 0600); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}

	_, _, loadErr := s.LoadOrError()
	if loadErr == nil {
		t.Fatal("expected error loading corrupt identity")
	}
	if loadErr == ErrStateNotFound {
		t.Fatal("corrupt identity must not be treated as not-found (silent regen)")
	}

	// Identity must still exist on disk (not deleted/regenerated).
	if !s.HasIdentity() {
		t.Error("corrupt identity file should not be removed")
	}

	// The stored public key must be untouched (key and identity stores are
	// separate; a corrupt identity does not destroy WG identity).
	privRaw, rerr := os.ReadFile(s.wgPrivatePath())
	if rerr != nil {
		t.Fatalf("read wg key: %v", rerr)
	}
	priv, derr := base64.StdEncoding.DecodeString(string(privRaw))
	if derr != nil {
		t.Fatalf("decode wg key: %v", derr)
	}
	defer Wipe(priv)
	kp, kerr := NewWgKeypair(priv)
	if kerr != nil {
		t.Fatalf("NewWgKeypair: %v", kerr)
	}
	if kp.PublicKeyBase64() != res.Key.PublicKeyBase64() {
		t.Error("WG identity changed after identity-file corruption")
	}
}

// TestCorruptWgKeyFailsSafe: a corrupt private-key file is surfaced, not
// silently replaced.
func TestCorruptWgKeyFailsSafe(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.CreateFresh("SparkBody", meta_()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	if err := os.WriteFile(s.wgPrivatePath(), []byte("not-base64!!!"), 0600); err != nil {
		t.Fatalf("write corrupt wg key: %v", err)
	}
	_, _, loadErr := s.LoadOrError()
	if loadErr == nil {
		t.Fatal("expected error loading corrupt wg key")
	}
}

// TestMissingWgKeyWithIdentityFailsSafe: an identity present without its WG
// key is an inconsistency surfaced as an error, never silently regenerated.
func TestMissingWgKeyWithIdentityFailsSafe(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.CreateFresh("SparkBody", meta_()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	// Remove the private key file.
	if err := os.Remove(s.wgPrivatePath()); err != nil {
		t.Fatalf("remove wg key: %v", err)
	}
	_, _, loadErr := s.LoadOrError()
	if loadErr == nil {
		t.Fatal("expected error when identity exists but WG key is missing")
	}
}

// TestPrivateKeyFileIsOwnerOnly: the WG private key is stored with
// restricted permissions so it stays local.
func TestPrivateKeyFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.CreateFresh("SparkBody", meta_()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	info, err := os.Stat(s.wgPrivatePath())
	if err != nil {
		t.Fatalf("stat wg key: %v", err)
	}
	mode := info.Mode() & 0o777
	// 0600: owner read/write only.
	if mode != 0o600 {
		t.Errorf("wg private key mode = 0%o, want 0600", mode)
	}
}

// TestNoIdentityFileInEndpoints: endpoint state never becomes part of the
// durable identity files.
func TestNoIdentityFileInEndpoints(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.CreateFresh("SparkBody", meta_()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	// Only identity + key files may exist; no endpoint/connection file.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range ents {
		name := e.Name()
		if strings.Contains(name, "endpoint") || strings.Contains(name, "connection") {
			t.Errorf("endpoint/connection state leaked into durable identity dir: %q", name)
		}
	}
}
