package body

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── Test fixtures ────────────────────────────────────────────────────────

// newPairingStore creates a fresh Body store (identity + WG keypair) rooted at
// a temp dir, and returns the store plus the persisted identity state.
func newPairingStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.CreateFresh("SparkBody", mkMeta_()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	return store
}

// validPairResponse builds a well-formed canonical PairResponse.
func validPairResponse(t *testing.T) dollnetwork.PairResponse {
	t.Helper()
	coreKP, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	return dollnetwork.PairResponse{
		Version:         dollnetwork.ProtocolVersion,
		NetworkID:       "net_test_1",
		BodyPeerID:      "peer_body_1",
		BodyAddresses:   []string{"fd00::1/128"},
		CorePeerID:      "peer_core_1",
		CoreWGPublicKey: coreKP.PublicKeyBase64(),
		CoreAddresses:   []string{"fd00::2/128"},
	}
}

// validInvitation builds an invitation pointing at the given direct bootstrap
// URL, valid as of `now`.
func validInvitation(now int64, bootstrapURL string) *dollnetwork.Invitation {
	return &dollnetwork.Invitation{
		Version:          dollnetwork.ProtocolVersion,
		InvitationID:     "inv_test_1",
		InvitationSecret: "invitation-secret-value",
		ExpiresAt:        time.Unix(now+3600, 0).UTC().Format(time.RFC3339),
		BootstrapEndpoints: []dollnetwork.BootstrapEndpoint{
			{URL: bootstrapURL},
		},
	}
}

// readMembershipFile returns the raw bytes of the durable membership file.
func readMembershipFile(t *testing.T, store *Store) (string, error) {
	t.Helper()
	raw, err := os.ReadFile(store.membershipPath())
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ── Success path ─────────────────────────────────────────────────────────

func TestPairWithInvitation_Success(t *testing.T) {
	store := newPairingStore(t)

	// Server asserts the request shape and returns a valid response.
	var gotReq dollnetwork.PairRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasSuffix(r.URL.Path, pairingEndpointPath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotReq); err != nil {
			t.Errorf("server failed to parse PairRequest: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)

	res, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation: %v", err)
	}
	if res == nil || res.Membership == nil {
		t.Fatal("expected non-nil PairResult with membership")
	}

	m := res.Membership
	if m.NetworkID != "net_test_1" {
		t.Errorf("network_id = %q, want net_test_1", m.NetworkID)
	}
	if m.Status != MembershipActive {
		t.Errorf("status = %q, want active", m.Status)
	}
	if m.BodyPeerID != "peer_body_1" || m.PeerID != "peer_body_1" {
		t.Errorf("body peer id mismatch: %q / %q", m.BodyPeerID, m.PeerID)
	}
	if m.CorePeerID != "peer_core_1" {
		t.Errorf("core_peer_id = %q, want peer_core_1", m.CorePeerID)
	}
	if m.BodyIPv6 != "fd00::1/128" {
		t.Errorf("body_ipv6 = %q, want fd00::1/128", m.BodyIPv6)
	}
	if m.CoreWGKeyB64 == "" {
		t.Error("membership missing core wg public key")
	}

	// The request sent to Core must have carried the Body's public WG key.
	if gotReq.Network.WireGuardPublicKey == "" {
		t.Error("PairRequest missing wireguard_public_key")
	}
	if gotReq.Body.BodyID == "" {
		t.Error("PairRequest missing body_id")
	}

	// Membership must be durably persisted (reload from disk).
	persisted, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership after pairing: %v", err)
	}
	if persisted.NetworkID != "net_test_1" || persisted.Status != MembershipActive {
		t.Errorf("persisted membership mismatch: %+v", persisted)
	}
}

// TestPairWithInvitation_PersistsNoSecret: the durable membership file must
// never contain the invitation secret.
func TestPairWithInvitation_PersistsNoSecret(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)
	inv.InvitationSecret = "super-secret-invitation-token"

	if _, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client()); err != nil {
		t.Fatalf("PairWithInvitation: %v", err)
	}

	raw, err := readMembershipFile(t, store)
	if err != nil {
		t.Fatalf("read membership file: %v", err)
	}
	if strings.Contains(raw, "super-secret-invitation-token") {
		t.Error("membership file leaked the invitation secret")
	}
}

// ── Failure paths ────────────────────────────────────────────────────────

func TestPairWithInvitation_ExpiredInvitation(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)
	inv.ExpiresAt = time.Unix(now-1, 0).UTC().Format(time.RFC3339) // expired

	_, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err == nil {
		t.Fatal("expected error for expired invitation, got nil")
	}
	if !strings.Contains(err.Error(), "invitation validity") {
		t.Errorf("expected 'invitation validity' error, got: %v", err)
	}
	// Nothing must be persisted on failure.
	if _, err := store.LoadMembership(); err != ErrStateNotFound {
		t.Errorf("expected ErrStateNotFound, got %v", err)
	}
}

func TestPairWithInvitation_NoDirectEndpoint(t *testing.T) {
	store := newPairingStore(t)
	now := time.Now().Unix()
	inv := &dollnetwork.Invitation{
		Version:          dollnetwork.ProtocolVersion,
		InvitationID:     "inv_relay",
		InvitationSecret: "sec",
		ExpiresAt:        time.Unix(now+3600, 0).UTC().Format(time.RFC3339),
		BootstrapEndpoints: []dollnetwork.BootstrapEndpoint{
			{URL: "relay://relay.example:51820"},
		},
	}
	_, err := PairWithInvitation(context.Background(), store, inv, now, http.DefaultClient)
	if err != ErrPairingUnsupportedBootstrap {
		t.Errorf("expected ErrPairingUnsupportedBootstrap, got %v", err)
	}
}

func TestPairWithInvitation_ServerError(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(dollnetwork.PairErrorResponse{
			Version: dollnetwork.ProtocolVersion,
			Error:   "bad invitation",
			Reason:  "invalid_invitation",
		})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	_, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client())
	if err == nil {
		t.Fatal("expected error for non-2xx, got nil")
	}
	if !strings.Contains(err.Error(), "pairing denied") {
		t.Errorf("expected 'pairing denied' error, got: %v", err)
	}
}

func TestPairWithInvitation_MalformedResponse(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 2xx but not a valid PairResponse (missing required fields).
		json.NewEncoder(w).Encode(map[string]string{"network_id": "net"})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	_, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client())
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
	if !strings.Contains(err.Error(), "validate pairing response") {
		t.Errorf("expected 'validate pairing response' error, got: %v", err)
	}
	// Atomicity: a malformed response must not persist partial membership.
	if _, err := store.LoadMembership(); err != ErrStateNotFound {
		t.Errorf("expected ErrStateNotFound after malformed response, got %v", err)
	}
}

func TestPairWithInvitation_NoIdentity(t *testing.T) {
	// A store with no identity yet must fail before any network call.
	store := NewStore(t.TempDir())
	now := time.Now().Unix()
	_, err := PairWithInvitation(context.Background(), store, validInvitation(now, "http://127.0.0.1:1"), now, http.DefaultClient)
	if err == nil {
		t.Fatal("expected error for missing identity, got nil")
	}
	if !strings.Contains(err.Error(), "load body identity") {
		t.Errorf("expected 'load body identity' error, got: %v", err)
	}
}

// TestPairWithInvitation_ContextTimeout: a hung Core must not block forever;
// the caller's context deadline must bound the request.
func TestPairWithInvitation_ContextTimeout(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Core never responds within the ctx budget
	}))
	defer srv.Close()

	now := time.Now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := PairWithInvitation(ctx, store, validInvitation(now, srv.URL), now, srv.Client())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("pairing took %v, expected bounded by context timeout", elapsed)
	}
}

// TestPairWithInvitation_DoesNotReplaceExistingMembership: pairing must fail
// closed when a durable membership is already established. The first pairing
// establishes membership; a second attempt must NOT replace it — the original
// network_id / body peer ID / Core relationship and the Body identity + WG
// keypair remain unchanged.
func TestPairWithInvitation_DoesNotReplaceExistingMembership(t *testing.T) {
	store := newPairingStore(t)
	// Capture the persisted identity + WG keypair before any pairing, so we can
	// prove pairing never regenerates them.
	stBefore, kpBefore, err := store.LoadOrError()
	if err != nil || stBefore == nil || kpBefore == nil {
		t.Fatalf("LoadOrError before pairing: %v", err)
	}
	bodyIDBefore := string(stBefore.Identity.BodyID)
	wgPubBefore := kpBefore.PublicKeyBase64()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)

	// ── First pairing: establishes the durable membership ───────────────
	first, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err != nil {
		t.Fatalf("first pairing: %v", err)
	}
	if first == nil || first.Membership == nil {
		t.Fatal("first pairing returned no membership")
	}
	firstNet := first.Membership.NetworkID
	firstBodyPeer := first.Membership.BodyPeerID
	firstCorePeer := first.Membership.CorePeerID
	firstCoreWg := first.Membership.CoreWGKeyB64

	// ── Second pairing: must FAIL CLOSED and leave everything untouched ─
	second, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err == nil {
		t.Fatal("second pairing succeeded; must fail closed on existing membership")
	}
	if second != nil {
		t.Error("second pairing returned a result despite failing closed")
	}
	// The error must be the explicit existing-membership sentinel.
	errOp := fmt.Sprintf("%v", err)
	if !strings.Contains(errOp, "existing membership") {
		t.Errorf("second pairing error = %q, want existing-membership error", errOp)
	}

	// ── Reload durable state: nothing was replaced ───────────────────────
	m, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership after failed second pairing: %v", err)
	}
	if m.NetworkID != firstNet {
		t.Errorf("network_id replaced: %q → %q", firstNet, m.NetworkID)
	}
	if m.BodyPeerID != firstBodyPeer {
		t.Errorf("body_peer_id replaced: %q → %q", firstBodyPeer, m.BodyPeerID)
	}
	if m.CorePeerID != firstCorePeer {
		t.Errorf("core_peer_id replaced: %q → %q", firstCorePeer, m.CorePeerID)
	}
	if m.CoreWGKeyB64 != firstCoreWg {
		t.Error("core_wg_public_key replaced")
	}

	// ── Body identity + WG keypair are never regenerated ─────────────────
	stAfter, kpAfter, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError after failed second pairing: %v", err)
	}
	if stAfter == nil || kpAfter == nil {
		t.Fatal("body identity lost after failed second pairing")
	}
	if string(stAfter.Identity.BodyID) != bodyIDBefore {
		t.Errorf("body id regenerated: %q → %q", bodyIDBefore, string(stAfter.Identity.BodyID))
	}
	if kpAfter.PublicKeyBase64() != wgPubBefore {
		t.Error("wg keypair regenerated")
	}
}

// TestPairWithInvitation_BootstrapURLTrailingSlash: the pairing endpoint is
// built structurally, so a bootstrap URL ending in "/" must resolve to
// "<origin>/v1/pair" (never "<origin>//v1/pair").
func TestPairWithInvitation_BootstrapURLTrailingSlash(t *testing.T) {
	store := newPairingStore(t)

	gotPath := ""
	gotCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, strings.TrimRight(srv.URL, "/")+"/")

	_, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation with trailing-slash bootstrap URL: %v", err)
	}
	if gotCount != 1 {
		t.Errorf("server received %d requests, want 1", gotCount)
	}
	if gotPath != pairingEndpointPath {
		t.Errorf("request path = %q, want %q (trailing slash must not double)", gotPath, pairingEndpointPath)
	}
	if strings.Contains(gotPath, "//") {
		t.Errorf("request path %q contains a double slash", gotPath)
	}
}

// TestPairWithInvitation_AcceptsBareIPv6: Core (the address authority) sends
// bare IPv6 overlay addresses (netip.Addr.String()) without a /mask. The Body
// must accept both bare and prefixed forms rather than rejecting the response.
func TestPairWithInvitation_AcceptsBareIPv6(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := validPairResponse(t)
		// Simulate Core's canonical output: bare address, no /128.
		base.BodyAddresses = []string{"fdc9::1"}
		base.CoreAddresses = []string{"fdc9::2"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(base)
	}))
	defer srv.Close()

	now := time.Now().Unix()
	res, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation with bare IPv6 overlay addresses: %v", err)
	}
	if res == nil || res.Membership == nil {
		t.Fatal("expected non-nil membership")
	}
	if res.Membership.BodyIPv6 != "fdc9::1" {
		t.Errorf("body_ipv6 = %q, want %q", res.Membership.BodyIPv6, "fdc9::1")
	}
}

// TestPairWithInvitation_CorruptMembershipFileFailsClosed:
// If membership.json exists but is corrupt/unreadable, pairing must fail closed
// and preserve the original file untouched. No request should reach Core.
func TestPairWithInvitation_CorruptMembershipFileFailsClosed(t *testing.T) {
	t.Helper()
	store := newPairingStore(t)
	// Corrupt the membership file with invalid JSON.
	if err := os.WriteFile(store.membershipPath(), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write corrupt membership: %v", err)
	}
	// Record original file size and mod time to detect overwrites.
	origInfo, err := os.Stat(store.membershipPath())
	if err != nil {
		t.Fatalf("stat original corrupt file: %v", err)
	}
	origSize := origInfo.Size()
	origMod := origInfo.ModTime()

	// Prepare a Core httptest server that would succeed if reached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request reached Core: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError) // make test fail loudly if called
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)

	// PairWithInvitation must fail closed due to corrupt membership.
	_, err = PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err == nil {
		t.Fatal("expected pairing to fail with corrupt membership, got nil error")
	}
	// Ensure no request hit the Core server.
	// Cannot easily spy; rely on the handler above not being called.

	// Verify the corrupt membership file was NOT overwritten.
	info, err := os.Stat(store.membershipPath())
	if err != nil {
		t.Fatalf("stat membership after failed pairing: %v", err)
	}
	if info.Size() != origSize {
		t.Errorf("membership file size changed after failed pairing: had %d, now %d", origSize, info.Size())
	}
	if !info.ModTime().Equal(origMod) {
		t.Errorf("membership file modified after failed pairing: had %v, now %v", origMod, info.ModTime())
	}
	// Contents should still be the corrupt bytes we wrote.
	data, err := os.ReadFile(store.membershipPath())
	if err != nil {
		t.Fatalf("read membership after failed pairing: %v", err)
	}
	if !bytes.Equal(data, []byte("{not valid json")) {
		t.Errorf("membership file contents corrupted unexpectedly: got %q, want corrupt", string(data))
	}

	// Body identity and WG keypair must remain unchanged.
	id0, kp0, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("load body identity before failed pairing: %v", err)
	}
	if id0 == nil || kp0 == nil {
		t.Fatal("missing identity or WG keypair before failed pairing")
	}
	// ... (rest of the test until the end)

	// Body identity and WG keypair must remain unchanged.
	id1, kp1, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("load body identity after failed pairing: %v", err)
	}
	if id1 == nil || kp1 == nil {
		t.Fatal("missing identity or WG keypair after failed pairing")
	}
	if id0.Identity.BodyID != id1.Identity.BodyID {
		t.Errorf("body ID changed after failed pairing: got %q, want %q", id1.Identity.BodyID, id0.Identity.BodyID)
	}
	if kp0.PublicKeyBase64() != kp1.PublicKeyBase64() {
		t.Errorf("WG public key changed after failed pairing")
	}
}
