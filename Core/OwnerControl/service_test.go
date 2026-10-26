package ownercontrol_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type fixture struct {
	store       persistence.Store
	network     *network.Network
	enrollment  *enrollment.Service
	permissions *permissionrequest.Manager
	owner       *ownercontrol.Service
	clock       *clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nw, err := network.NewNetwork("owner-control-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(network.NetworkStore).SaveNetwork(context.Background(), nw); err != nil {
		t.Fatal(err)
	}
	clock := &clock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	invitations := invitation.NewService(store.(invitation.Store), clock)
	boundary := ownercontrol.NewBoundary()
	enrollments, err := enrollment.NewService(context.Background(), store.(enrollment.Store), nw, invitations, boundary, clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	permissions := permissionrequest.NewWithClock(store.(permissionrequest.Store), actionpolicy.Default(), clock)
	access := permissionrequest.NewAccessService(permissions, boundary)
	return &fixture{store: store, network: nw, enrollment: enrollments, permissions: permissions,
		owner: ownercontrol.NewService(enrollments, invitations, boundary, access), clock: clock}
}

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
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

func begin(t *testing.T, fx *fixture, requestID string) (string, ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	publicKey, privateKey, wg := keys(t)
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	request := dollnetwork.EnrollmentBeginRequest{Version: 1, RequestID: requestID, CoreID: fx.enrollment.CoreIdentity().CoreID, BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), WGPublicKey: wg, BodyNonce: dollnetwork.EncodeRawURL(nonce), Metadata: dollnetwork.EnrollmentMetadata{"name": requestID}}
	if err := dollnetwork.SignEnrollmentBegin(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	pending, err := fx.enrollment.Begin(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return pending.ApprovalCode, publicKey, privateKey, wg
}

func TestLocalOwnerEnrollmentOperations(t *testing.T) {
	fx := newFixture(t)
	code, _, _, _ := begin(t, fx, "approve-me")
	duplicatePublic, duplicatePrivate, duplicateWG := keys(t)
	duplicateNonce := make([]byte, 32)
	_, _ = rand.Read(duplicateNonce)
	duplicate := dollnetwork.EnrollmentBeginRequest{Version: 1, RequestID: "approve-me", CoreID: fx.enrollment.CoreIdentity().CoreID, BodyPublicKey: dollnetwork.EncodeRawURL(duplicatePublic), WGPublicKey: duplicateWG, BodyNonce: dollnetwork.EncodeRawURL(duplicateNonce)}
	if err := dollnetwork.SignEnrollmentBegin(duplicatePrivate, &duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.enrollment.Begin(context.Background(), duplicate); !errors.Is(err, enrollment.ErrReplayed) {
		t.Fatalf("duplicate request error = %v", err)
	}
	if _, err := fx.enrollment.ListPending(context.Background(), "local-admin"); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("untrusted direct owner call error = %v", err)
	}
	if _, err := fx.enrollment.Approve(context.Background(), code, "local-admin", nil); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("untrusted approval error = %v", err)
	}
	pending, err := fx.owner.ListPending(context.Background())
	if err != nil || len(pending) != 1 || len(pending[0].CodeVerifier) != 0 || len(pending[0].BodyNonce) != 0 {
		t.Fatalf("pending list = %#v, %v", pending, err)
	}
	receipt, err := fx.owner.Approve(context.Background(), code, []string{"sensor.read"})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := fx.owner.Approve(context.Background(), code, []string{"sensor.read"})
	if err != nil || retried.BodyID != receipt.BodyID {
		t.Fatalf("duplicate approval = %#v, %v", retried, err)
	}
	bodies, err := fx.owner.ListBodies(context.Background())
	if err != nil || len(bodies) != 1 || bodies[0].Status != "enrolled" {
		t.Fatalf("Body list = %#v, %v", bodies, err)
	}

	denyCode, _, _, _ := begin(t, fx, "deny-me")
	if err := fx.owner.Deny(context.Background(), denyCode); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.owner.Approve(context.Background(), denyCode, nil); !errors.Is(err, enrollment.ErrDenied) {
		t.Fatalf("approve denied request error = %v", err)
	}
	expiredCode, _, _, _ := begin(t, fx, "expire-me")
	fx.clock.now = fx.clock.now.Add(11 * time.Minute)
	if _, err := fx.owner.Approve(context.Background(), expiredCode, nil); !errors.Is(err, enrollment.ErrExpired) {
		t.Fatalf("expired approval error = %v", err)
	}
}

func TestOwnerInvitationIsExpiringAndSingleUse(t *testing.T) {
	fx := newFixture(t)
	created, err := fx.owner.CreateInvitation(context.Background(), time.Minute, []string{"lights.set"}, dollnetwork.Endpoints{{URL: "https://core.example"}})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, wg := keys(t)
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	request := dollnetwork.EnrollmentRedeemRequest{Version: 1, RequestID: "invite-body", CoreID: fx.enrollment.CoreIdentity().CoreID, InvitationID: created.ID, InvitationSecret: created.Secret, BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), WGPublicKey: wg, BodyNonce: dollnetwork.EncodeRawURL(nonce)}
	if err := dollnetwork.SignEnrollmentRedeem(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.enrollment.Redeem(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "invite-replay"
	if err := dollnetwork.SignEnrollmentRedeem(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.enrollment.Redeem(context.Background(), request); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("invitation replay error = %v", err)
	}

	expired, err := fx.owner.CreateInvitation(context.Background(), time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fx.clock.now = fx.clock.now.Add(2 * time.Minute)
	expiredKey, expiredPrivate, expiredWG := keys(t)
	expiredNonce := make([]byte, 32)
	_, _ = rand.Read(expiredNonce)
	expiredRequest := dollnetwork.EnrollmentRedeemRequest{Version: 1, RequestID: "expired-invite", CoreID: fx.enrollment.CoreIdentity().CoreID, InvitationID: expired.ID, InvitationSecret: expired.Secret, BodyPublicKey: dollnetwork.EncodeRawURL(expiredKey), WGPublicKey: expiredWG, BodyNonce: dollnetwork.EncodeRawURL(expiredNonce)}
	if err := dollnetwork.SignEnrollmentRedeem(expiredPrivate, &expiredRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.enrollment.Redeem(context.Background(), expiredRequest); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("expired invitation error = %v", err)
	}
}

func TestUnixOwnerControlClientAndSocketPermissions(t *testing.T) {
	fx := newFixture(t)
	begin(t, fx, "socket-pending")
	permission, err := fx.permissions.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "owner-control-test", OwnerID: "local-admin", Origin: permissionrequest.OriginVoluntary,
		Question: "May I continue?", ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "admin.sock")
	server, err := ownercontrol.NewServer(path, fx.owner)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		<-done
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %v", info.Mode())
	}
	client := ownercontrol.NewClient(path)
	pending, err := client.ListPending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("local client pending = %#v, %v", pending, err)
	}
	permissions, err := client.ListPermissionRequests(context.Background())
	if err != nil || len(permissions) != 1 || permissions[0].ID != permission.ID {
		t.Fatalf("permission list = %#v, %v", permissions, err)
	}
	detail, err := client.GetPermissionRequest(context.Background(), permission.ID)
	if err != nil || detail.ID != permission.ID || detail.Question != permission.Question {
		t.Fatalf("permission detail = %#v, %v", detail, err)
	}
}
