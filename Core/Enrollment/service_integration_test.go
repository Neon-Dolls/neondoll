package enrollment_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

type fixture struct {
	service     *enrollment.Service
	store       persistence.Store
	network     *network.Network
	invitations *invitation.Service
	clock       *fixedClock
}

func newFixture(t *testing.T, path string) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	nw, err := network.NewNetwork("enrollment-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(network.NetworkStore).SaveNetwork(ctx, nw); err != nil {
		t.Fatal(err)
	}
	invStore := store.(invitation.Store)
	clock := &fixedClock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	invSvc := invitation.NewService(invStore, clock)
	service, err := enrollment.NewService(ctx, store.(enrollment.Store), nw, invSvc, enrollment.AllowOwnerAuthorizer{}, clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{service: service, store: store, network: nw, invitations: invSvc, clock: clock}
}

func bodyKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wg := make([]byte, 32)
	if _, err := rand.Read(wg); err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey, base64.StdEncoding.EncodeToString(wg)
}

func beginRequest(t *testing.T, service *enrollment.Service, requestID string, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey, wg string) dollnetwork.EnrollmentBeginRequest {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	request := dollnetwork.EnrollmentBeginRequest{
		Version: 1, RequestID: requestID, CoreID: service.CoreIdentity().CoreID,
		BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), WGPublicKey: wg,
		BodyNonce: dollnetwork.EncodeRawURL(nonce), Metadata: dollnetwork.EnrollmentMetadata{"name": "desk"},
	}
	if err := dollnetwork.SignEnrollmentBegin(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func resultRequest(t *testing.T, service *enrollment.Service, requestID string, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) dollnetwork.EnrollmentResultRequest {
	t.Helper()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	request := dollnetwork.EnrollmentResultRequest{
		Version: 1, RequestID: requestID, CoreID: service.CoreIdentity().CoreID,
		BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), BodyNonce: dollnetwork.EncodeRawURL(nonce),
	}
	if err := dollnetwork.SignEnrollmentResult(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestBodyInitiatedEnrollmentPersistsIdentityMembershipAndRecoverableReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.db")
	fx := newFixture(t, path)
	publicKey, privateKey, wg := bodyKeys(t)
	pending, err := fx.service.Begin(context.Background(), beginRequest(t, fx.service, "request-body", publicKey, privateKey, wg))
	if err != nil {
		t.Fatal(err)
	}
	if err := dollnetwork.VerifyEnrollmentPending(fx.service.CoreIdentity().PublicKey, *pending); err != nil {
		t.Fatalf("verify pending response: %v", err)
	}
	receipt, err := fx.service.Approve(context.Background(), pending.ApprovalCode, "owner-1", []string{"lights.set"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dollnetwork.VerifyEnrollmentReceipt(fx.service.CoreIdentity().PublicKey, *receipt); err != nil {
		t.Fatalf("verify receipt: %v", err)
	}
	if receipt.BodyID == "" || receipt.MembershipReference == "" {
		t.Fatalf("incomplete receipt: %#v", receipt)
	}
	recoveryRequest := resultRequest(t, fx.service, "request-body", publicKey, privateKey)
	result, err := fx.service.Result(context.Background(), recoveryRequest)
	if err != nil || result.Receipt == nil || result.Receipt.BodyID != receipt.BodyID {
		t.Fatalf("recover result = %#v, %v", result, err)
	}
	body, err := fx.store.(enrollment.Store).GetBody(context.Background(), receipt.BodyID)
	if err != nil || body == nil || !body.PublicKey.Equal(publicKey) {
		t.Fatalf("durable Body credential = %#v, %v", body, err)
	}
	membership := fx.network.GetMembership(network.PeerID(receipt.MembershipReference))
	if membership == nil || membership.BodyID != receipt.BodyID || membership.Status != network.MembershipActive {
		t.Fatalf("membership = %#v", membership)
	}

	coreID := fx.service.CoreIdentity()
	publicCoreID := fx.service.PublicCoreIdentity()
	if err := fx.store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	nw2, err := store2.(network.NetworkStore).LoadNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if nw2 == nil {
		t.Fatal("network identity missing after restart")
	}
	service2, err := enrollment.NewService(context.Background(), store2.(enrollment.Store), nw2, invitation.NewService(store2.(invitation.Store), fx.clock), enrollment.AllowOwnerAuthorizer{}, fx.clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if service2.CoreIdentity().CoreID != coreID.CoreID || !service2.CoreIdentity().PublicKey.Equal(coreID.PublicKey) {
		t.Fatal("Core signing identity changed across restart")
	}
	if service2.PublicCoreIdentity() != publicCoreID {
		t.Fatal("public Core identity discovery changed across restart")
	}
	if _, err := service2.Result(context.Background(), recoveryRequest); !errors.Is(err, enrollment.ErrReplayed) {
		t.Fatalf("persisted result nonce replay error = %v", err)
	}
	recovered, err := service2.Result(context.Background(), resultRequest(t, service2, "request-body", publicKey, privateKey))
	if err != nil || recovered.Receipt.BodyID != receipt.BodyID {
		t.Fatalf("restart recovery = %#v, %v", recovered, err)
	}
}

func TestInvitationEnrollmentIsSingleUseAndProofBound(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	created, err := fx.invitations.Create(context.Background(), invitation.CreateParams{Lifetime: time.Minute, Permissions: []string{"sensor.read"}})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, wg := bodyKeys(t)
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	request := dollnetwork.EnrollmentRedeemRequest{
		Version: 1, RequestID: "invitation-request", CoreID: fx.service.CoreIdentity().CoreID,
		InvitationID: created.ID, InvitationSecret: created.Secret,
		BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), WGPublicKey: wg, BodyNonce: dollnetwork.EncodeRawURL(nonce),
	}
	if err := dollnetwork.SignEnrollmentRedeem(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	receipt, err := fx.service.Redeem(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := fx.service.Redeem(context.Background(), request)
	if err != nil || retried.BodyID != receipt.BodyID {
		t.Fatalf("authorized retry = %#v, %v", retried, err)
	}
	wrongSecret := request
	wrongSecret.InvitationSecret = "different-secret"
	if err := dollnetwork.SignEnrollmentRedeem(privateKey, &wrongSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.service.Redeem(context.Background(), wrongSecret); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("wrong-secret enrolled retry error = %v", err)
	}
	request.RequestID = "replay-with-new-request"
	if err := dollnetwork.SignEnrollmentRedeem(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.service.Redeem(context.Background(), request); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("replay error = %v", err)
	}
	if len(fx.network.ListMemberships()) != 1 || fx.network.ListMemberships()[0].BodyID != receipt.BodyID {
		t.Fatalf("invitation replay created membership: %#v", fx.network.ListMemberships())
	}
}

func TestConcurrentApprovalReturnsOneDurableBody(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	publicKey, privateKey, wg := bodyKeys(t)
	pending, err := fx.service.Begin(context.Background(), beginRequest(t, fx.service, "concurrent", publicKey, privateKey, wg))
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	results := make(chan *dollnetwork.EnrollmentReceipt, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, err := fx.service.Approve(context.Background(), pending.ApprovalCode, "owner", nil)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- receipt
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent approval: %v", err)
	}
	var bodyID string
	for receipt := range results {
		if bodyID == "" {
			bodyID = receipt.BodyID
		}
		if receipt.BodyID != bodyID {
			t.Fatalf("duplicate Body identities: %q and %q", bodyID, receipt.BodyID)
		}
	}
	if got := len(fx.network.ListMemberships()); got != 1 {
		t.Fatalf("memberships = %d, want 1", got)
	}
}

func TestInvalidProofAndExpiryFailClosed(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	publicKey, privateKey, wg := bodyKeys(t)
	request := beginRequest(t, fx.service, "bad-proof", publicKey, privateKey, wg)
	request.CoreID = "wrong-core"
	if _, err := fx.service.Begin(context.Background(), request); !errors.Is(err, enrollment.ErrCoreIdentity) {
		t.Fatalf("wrong Core error = %v", err)
	}
	pending, err := fx.service.Begin(context.Background(), beginRequest(t, fx.service, "expires", publicKey, privateKey, wg))
	if err != nil {
		t.Fatal(err)
	}
	fx.clock.now = pending.ExpiresAt
	if _, err := fx.service.Approve(context.Background(), pending.ApprovalCode, "owner", nil); !errors.Is(err, enrollment.ErrExpired) {
		t.Fatalf("expired approval error = %v", err)
	}
	if len(fx.network.ListMemberships()) != 0 {
		t.Fatal("expired request created membership")
	}
}

func TestApprovalFailsClosedWithoutOwnerAuthorizer(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	service, err := enrollment.NewService(context.Background(), fx.store.(enrollment.Store), fx.network, fx.invitations, nil, fx.clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, wg := bodyKeys(t)
	pending, err := service.Begin(context.Background(), beginRequest(t, service, "no-owner-authority", publicKey, privateKey, wg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), pending.ApprovalCode, "claimed-owner", nil); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("approval error = %v", err)
	}
	if len(fx.network.ListMemberships()) != 0 {
		t.Fatal("unauthorized owner created membership")
	}
}
