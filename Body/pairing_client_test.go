package body

import (
	"context"
	"encoding/json"
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
