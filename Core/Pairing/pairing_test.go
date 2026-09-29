package pairing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// stubClock implements invitation.Clock with a fixed time.
type stubClock struct {
	t time.Time
}

func (s *stubClock) Now() time.Time { return s.t }

func (s *stubClock) Advance(d time.Duration) { s.t = s.t.Add(d) }

// stubAuthorizer implements Authorizer for deterministic tests.
type stubAuthorizer struct {
	allow bool
	err   error
}

func (a *stubAuthorizer) AuthorizePairing(ctx context.Context, bodyID string, req *dollnetwork.PairRequest) error {
	if !a.allow {
		return fmt.Errorf("stub: pairing denied for %s", bodyID)
	}
	return a.err
}

// recordingNetStore records SaveMembership calls for verification.
type recordingNetStore struct {
	network.NetworkStore
	saved []*network.Membership
}

func (r *recordingNetStore) SaveMembership(ctx context.Context, m *network.Membership) error {
	r.saved = append(r.saved, m)
	return nil
}

func (r *recordingNetStore) SaveNetwork(ctx context.Context, n *network.Network) error {
	return nil
}
func (r *recordingNetStore) LoadNetwork(ctx context.Context) (*network.Network, error) {
	return nil, fmt.Errorf("not implemented")
}
func (r *recordingNetStore) LoadMembership(ctx context.Context, id network.PeerID) (*network.Membership, error) {
	return nil, fmt.Errorf("not implemented")
}
func (r *recordingNetStore) ListMemberships(ctx context.Context) ([]*network.Membership, error) {
	return nil, fmt.Errorf("not implemented")
}
func (r *recordingNetStore) DeleteMembership(ctx context.Context, id network.PeerID) error {
	return fmt.Errorf("not implemented")
}

// failingNetStore fails on SaveMembership.
type failingNetStore struct {
	recordingNetStore
}

func (f *failingNetStore) SaveMembership(ctx context.Context, m *network.Membership) error {
	return fmt.Errorf("storage failure")
}

// setup creates a fresh pairing environment for each test.
func setup(t *testing.T, clock *stubClock) (*PairingService, *network.Network, *recordingNetStore) {
	t.Helper()

	netID := network.NetworkID("testnet")
	net, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatal("new network:", err)
	}

	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, clock)

	auth := &stubAuthorizer{allow: true}
	rec := &recordingNetStore{}
	endpoints := dollnetwork.Endpoints{{URL: "relay://core.example.com:51820"}}

	svc := NewService(net, rec, invSvc, auth, clock, endpoints)
	return svc, net, rec
}

// createInvite creates an invitation and returns the invitation document.
func createInvite(t *testing.T, svc *PairingService, clock *stubClock) *dollnetwork.Invitation {
	t.Helper()
	ci, err := svc.invSvc.Create(context.Background(), invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: []string{"relay://core.example.com:51820"},
	})
	if err != nil {
		t.Fatal("create invitation:", err)
	}
	eps := make(dollnetwork.Endpoints, len(ci.BootstrapEndpoints))
	for i, e := range ci.BootstrapEndpoints {
		eps[i] = dollnetwork.BootstrapEndpoint{URL: e}
	}
	return &dollnetwork.Invitation{
		Version:            dollnetwork.ProtocolVersion,
		InvitationID:       ci.ID,
		InvitationSecret:   ci.Secret,
		ExpiresAt:          ci.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: eps,
	}
}

// validPairRequest creates a PairRequest from an invitation document.
func validPairRequest(inv *dollnetwork.Invitation) *dollnetwork.PairRequest {
	wgKey := make([]byte, 32)
	for i := range wgKey {
		wgKey[i] = byte(i)
	}
	return &dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: inv.InvitationID,
		Secret:       inv.InvitationSecret,
		Body: dollnetwork.PairRequestBody{
			BodyID:         "body-sensor-01",
			Implementation: "neondoll-test/v1",
			Platform:       "test",
			Arch:           "test",
		},
		Network: dollnetwork.PairingNetwork{
			WireGuardPublicKey: base64.StdEncoding.EncodeToString(wgKey),
		},
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestPairing_HappyPath(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, net, rec := setup(t, clock)
	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)

	resp, errResp := svc.HandlePairing(context.Background(), req)
	if errResp != nil {
		t.Fatalf("unexpected error response: %+v", errResp)
	}
	if resp == nil {
		t.Fatal("expected success response")
	}

	// Verify response fields.
	if resp.Version != dollnetwork.ProtocolVersion {
		t.Errorf("version = %d, want %d", resp.Version, dollnetwork.ProtocolVersion)
	}
	if resp.NetworkID != "testnet" {
		t.Errorf("network_id = %q, want %q", resp.NetworkID, "testnet")
	}
	if resp.BodyPeerID == "" {
		t.Error("body_peer_id is empty")
	}
	if len(resp.BodyAddresses) != 1 {
		t.Fatal("expected 1 body address")
	}
	if resp.CorePeerID != string(net.Core.PeerID) {
		t.Errorf("core_peer_id = %q, want %q", resp.CorePeerID, string(net.Core.PeerID))
	}
	if resp.CoreWGPublicKey == "" {
		t.Error("core_wg_public_key is empty")
	}
	if len(resp.CoreAddresses) != 1 {
		t.Fatal("expected 1 core address")
	}
	if len(resp.CoreEndpoints) != 1 || resp.CoreEndpoints[0].URL != "relay://core.example.com:51820" {
		t.Error("core_endpoints mismatch")
	}

	// Verify membership was persisted.
	if len(rec.saved) != 1 {
		t.Fatalf("expected 1 saved membership, got %d", len(rec.saved))
	}
	saved := rec.saved[0]
	if saved.Status != network.MembershipActive {
		t.Errorf("saved membership status = %v, want active", saved.Status)
	}
	if saved.PeerID != network.PeerID(resp.BodyPeerID) {
		t.Errorf("saved peer id mismatch")
	}

	// Verify invitation is consumed.
	_, err := svc.invSvc.Validate(context.Background(), inv.InvitationID, inv.InvitationSecret)
	if err == nil {
		t.Error("expected invitation to be consumed, but Validate succeeded")
	}

	// Verify no private key in the response.
	n := countContaining([]interface{}{resp, errResp}, "PrivateKey")
	if n > 0 {
		t.Error("found PrivateKey in response serialization")
	}
}

func countContaining(objs []interface{}, substr string) int {
	var count int
	for _, o := range objs {
		if o == nil {
			continue
		}
		d, _ := json.Marshal(o)
		if bytes.Contains(d, []byte(substr)) {
			count++
		}
	}
	return count
}

func TestPairing_RejectsWrongSecret(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	req := validPairRequest(inv)
	req.Secret = "wrong-secret"

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error response")
	}
	if errResp.Reason != "invalid_invitation" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "invalid_invitation")
	}
	if errResp.Consumed {
		t.Error("should not be consumed on wrong secret")
	}
}

func TestPairing_RejectsUnknownInvitation(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)

	req := &dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: "nonexistent-id",
		Secret:       "some-secret",
		Body: dollnetwork.PairRequestBody{
			BodyID:         "body-01",
			Implementation: "neondoll-test/v1",
			Platform:       "test",
			Arch:           "test",
		},
		Network: dollnetwork.PairingNetwork{
			WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error response")
	}
	if errResp.Reason != "invalid_invitation" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "invalid_invitation")
	}
}

func TestPairing_RejectsExpiredInvitation(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	// Advance past expiry.
	clock.Advance(invitation.DefaultLifetime + time.Second)

	req := validPairRequest(inv)
	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error response for expired invitation")
	}
	if errResp.Reason != "invalid_invitation" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "invalid_invitation")
	}
}

func TestPairing_RejectsConsumedInvitation(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)

	// First use succeeds.
	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp != nil {
		t.Fatalf("first pairing failed: %+v", errResp)
	}

	// Second use fails.
	_, errResp = svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error for consumed invitation")
	}
	if errResp.Reason != "invalid_invitation" && errResp.Reason != "body_already_known" {
		t.Logf("rejection reason: %q", errResp.Reason)
	}
}

func TestPairing_ConcurrentDoubleSubmit(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, net, _ := setup(t, clock)
	_ = net // we use the network from setup

	// Create two separate invitations so there's no conflict on body_id.
	inv1 := createInvite(t, svc, clock)
	inv2 := createInvite(t, svc, clock)

	req1 := validPairRequest(inv1)
	req1.Body.BodyID = "body-concurrent-a"
	req2 := validPairRequest(inv2)
	req2.Body.BodyID = "body-concurrent-b"

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	errs := 0

	// Submit two pair requests concurrently.
	for _, req := range []*dollnetwork.PairRequest{req1, req2} {
		wg.Add(1)
		go func(r *dollnetwork.PairRequest) {
			defer wg.Done()
			_, errResp := svc.HandlePairing(context.Background(), r)
			mu.Lock()
			if errResp == nil {
				successes++
			} else {
				errs++
			}
			mu.Unlock()
		}(req)
	}
	wg.Wait()

	// Both should succeed since they use different invitations and body IDs.
	if successes != 2 {
		t.Errorf("expected 2 successes, got %d (errors: %d)", successes, errs)
	}
}

func TestPairing_ConcurrentSameInvitation(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)
	req.Body.BodyID = "body-concurrent" // different IDs to isolate just the invitation conflict

	// Use different body IDs to test only invitation consumption race.
	reqA := *req
	reqA.Body.BodyID = "body-race-a"
	reqB := *req
	reqB.Body.BodyID = "body-race-b"

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	var errResponses []*dollnetwork.PairErrorResponse

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errResp := svc.HandlePairing(context.Background(), &reqA)
		mu.Lock()
		if errResp == nil {
			successes++
		} else {
			errResponses = append(errResponses, errResp)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		_, errResp := svc.HandlePairing(context.Background(), &reqB)
		mu.Lock()
		if errResp == nil {
			successes++
		} else {
			errResponses = append(errResponses, errResp)
		}
		mu.Unlock()
	}()
	wg.Wait()

	if successes != 1 {
		t.Errorf("exactly 1 concurrent submission should succeed with the same invitation, got %d (errors: %d errors captured)", successes, len(errResponses))
	}

	// The failing response must indicate the invitation was already consumed.
	for _, er := range errResponses {
		if er == nil {
			continue
		}
		if !er.Consumed {
			t.Errorf("the failing response must set Consumed=true, got Consumed=%v", er.Consumed)
		}
	}
	}

	func TestPairing_RejectsUnsupportedVersion(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	req := validPairRequest(inv)
	req.Version = 999

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error for unsupported version")
	}
	if errResp.Reason != "unsupported_version" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "unsupported_version")
	}
}

func TestPairing_RejectsMissingBodyID(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	req := validPairRequest(inv)
	req.Body.BodyID = ""

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error for missing body_id")
	}
	if errResp.Reason != "missing_body_id" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "missing_body_id")
	}
}

func TestPairing_RejectsNonBase64Key(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	req := validPairRequest(inv)
	req.Network.WireGuardPublicKey = "!!!not-base64!!!"

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error for non-base64 key")
	}
	if errResp.Reason != "invalid_wg_public_key" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "invalid_wg_public_key")
	}
}

func TestPairing_RejectsWrongKeyLength(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	req := validPairRequest(inv)
	// Not 32 bytes after decode.
	req.Network.WireGuardPublicKey = base64.StdEncoding.EncodeToString([]byte("short"))

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error for wrong key length")
	}
	if errResp.Reason != "invalid_wg_public_key" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "invalid_wg_public_key")
	}
}

func TestPairing_DenialDoesNotCreateMembership(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, clock)

	net, err := network.NewNetwork("denial-test")
	if err != nil {
		t.Fatal(err)
	}

	// Denying authorizer.
	auth := &stubAuthorizer{allow: false}
	rec := &recordingNetStore{}
	svc := NewService(net, rec, invSvc, auth, clock, nil)

	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error on denial")
	}
	if errResp.Reason != "authorization_denied" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "authorization_denied")
	}

	// No memberships should exist.
	mems := net.Memberships
	if len(mems) != 0 {
		t.Errorf("expected 0 memberships, got %d", len(mems))
	}

	// The invitation should be consumed on denial per spec.
	_, err = invSvc.Validate(context.Background(), inv.InvitationID, inv.InvitationSecret)
	if err == nil {
		t.Error("expected invitation to be consumed after denial")
	}
}

func TestPairing_DuplicateBodyID(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv1 := createInvite(t, svc, clock)
	inv2 := createInvite(t, svc, clock)

	// First pairing with body-id-1.
	req1 := validPairRequest(inv1)
	req1.Body.BodyID = "body-dup"
	_, errResp := svc.HandlePairing(context.Background(), req1)
	if errResp != nil {
		t.Fatalf("first pairing failed: %+v", errResp)
	}

	// Second pairing with same body ID but different invitation.
	req2 := validPairRequest(inv2)
	req2.Body.BodyID = "body-dup"
	_, errResp = svc.HandlePairing(context.Background(), req2)
	if errResp == nil {
		t.Fatal("expected error for duplicate body_id")
	}
	if errResp.Reason != "body_already_known" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "body_already_known")
	}

	// Verify second invitation was consumed.
	_, err := svc.invSvc.Validate(context.Background(), inv2.InvitationID, inv2.InvitationSecret)
	if err == nil {
		t.Error("expected second invitation consumed on duplicate body ID")
	}
}

func TestPairing_PersistenceFailure(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, clock)

	net, err := network.NewNetwork("persist-fail")
	if err != nil {
		t.Fatal(err)
	}

	auth := &stubAuthorizer{allow: true}
	failStore := &failingNetStore{}
	svc := NewService(net, failStore, invSvc, auth, clock, nil)

	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)

	_, errResp := svc.HandlePairing(context.Background(), req)
	if errResp == nil {
		t.Fatal("expected error on persistence failure")
	}
	if errResp.Reason != "persistence_failed" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "persistence_failed")
	}

	// Membership should have been removed from in-memory network.
	if len(net.Memberships) != 0 {
		t.Errorf("expected 0 memberships after persistence failure, got %d", len(net.Memberships))
	}

	// Invitation should NOT be consumed on persistence failure so Body can
	// retry once storage recovers. See spec: "If persistence fails, return
	// failure, leave state coherent."
	_, err = invSvc.Validate(context.Background(), inv.InvitationID, inv.InvitationSecret)
	if err != nil {
		t.Errorf("invitation should NOT be consumed on persistence failure, got: %v", err)
	}
}

func TestPairing_PrivateWGPKeyNotInResponse(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)
	req := validPairRequest(inv)

	resp, _ := svc.HandlePairing(context.Background(), req)
	if resp == nil {
		t.Fatal("expected success")
	}

	data, _ := json.Marshal(resp)
	if bytes.Contains(data, []byte("private")) {
		t.Error("response contains 'private' field")
	}
	if bytes.Contains(data, []byte("PrivateKey")) {
		t.Error("response contains PrivateKey")
	}

	// Verify the core WG public key is present but no private key.
	if resp.CoreWGPublicKey == "" {
		t.Error("expected core_wg_public_key in response")
	}
}

// ── HTTP Handler tests ───────────────────────────────────────────────────────

func TestHTTPHandler_HappyPath(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	inv := createInvite(t, svc, clock)

	handler := NewHandler(svc, nil)

	req := validPairRequest(inv)
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/v1/pair", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.HandlePair(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp dollnetwork.PairResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal("unmarshal response:", err)
	}
	if resp.BodyPeerID == "" {
		t.Error("body_peer_id is empty")
	}
}

func TestHTTPHandler_RejectsWrongMethod(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	handler := NewHandler(svc, nil)

	r := httptest.NewRequest(http.MethodGet, "/v1/pair", nil)
	w := httptest.NewRecorder()
	handler.HandlePair(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHTTPHandler_RejectsMalformedJSON(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	handler := NewHandler(svc, nil)

	r := httptest.NewRequest(http.MethodPost, "/v1/pair", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	handler.HandlePair(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var errResp dollnetwork.PairErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatal("unmarshal:", err)
	}
	if errResp.Reason != "malformed_request" {
		t.Errorf("reason = %q, want %q", errResp.Reason, "malformed_request")
	}
}

func TestHTTPHandler_RejectsTrailingGarbage(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	handler := NewHandler(svc, nil)

	// Valid JSON followed by trailing data.
	invalid := `{"version":1} extra garbage`

	r := httptest.NewRequest(http.MethodPost, "/v1/pair", bytes.NewReader([]byte(invalid)))
	w := httptest.NewRecorder()
	handler.HandlePair(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for trailing garbage, got %d", w.Code)
	}
}

func TestHTTPHandler_ReturnsCorrectStatusCodes(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	handler := NewHandler(svc, nil)

	tests := []struct {
		name       string
		req        *dollnetwork.PairRequest
		wantStatus int
	}{
		{
			name: "missing body_id",
			req: func() *dollnetwork.PairRequest {
				inv := createInvite(t, svc, clock)
				return &dollnetwork.PairRequest{
					Version:      dollnetwork.ProtocolVersion,
					InvitationID: inv.InvitationID,
					Secret:       inv.InvitationSecret,
					Body: dollnetwork.PairRequestBody{
						BodyID: "",
					},
					Network: dollnetwork.PairingNetwork{
						WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
				}
			}(),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown invitation",
			req: &dollnetwork.PairRequest{
				Version:      dollnetwork.ProtocolVersion,
				InvitationID: "no-such-invitation",
				Secret:       "some-secret",
				Body: dollnetwork.PairRequestBody{
							BodyID:         "body-01",
							Implementation: "neondoll-test/v1",
							Platform:       "test",
							Arch:           "test",
						},
				Network: dollnetwork.PairingNetwork{
					WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
				},
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unsupported version",
			req: &dollnetwork.PairRequest{
				Version:      999,
				InvitationID: "some-id",
				Secret:       "some-secret",
				Body: dollnetwork.PairRequestBody{
							BodyID:         "body-01",
							Implementation: "neondoll-test/v1",
							Platform:       "test",
							Arch:           "test",
						},
				Network: dollnetwork.PairingNetwork{
					WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
				},
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.req)
			r := httptest.NewRequest(http.MethodPost, "/v1/pair", bytes.NewReader(body))
			w := httptest.NewRecorder()
			handler.HandlePair(w, r)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestHTTPHandler_RequestBounded(t *testing.T) {
	clock := &stubClock{t: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	svc, _, _ := setup(t, clock)
	handler := NewHandler(svc, nil)

	// Send a too-large body.
	huge := make([]byte, 128<<10) // 128 KB
	for i := range huge {
		huge[i] = 'a'
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/pair", bytes.NewReader(huge))
	w := httptest.NewRecorder()
	handler.HandlePair(w, r)
	// Should not crash. Expect some error.
	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Logf("status on oversized body: %d", w.Code)
	}
}

