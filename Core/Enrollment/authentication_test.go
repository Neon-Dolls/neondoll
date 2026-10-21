package enrollment_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

func enrollBody(t *testing.T, fx *fixture, requestID string) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, wg := bodyKeys(t)
	pending, err := fx.service.Begin(context.Background(), beginRequest(t, fx.service, requestID, publicKey, privateKey, wg))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fx.service.Approve(context.Background(), pending.ApprovalCode, "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	return receipt.BodyID, privateKey
}

func hello(t *testing.T, bodyID string) authentication.Hello {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return authentication.Hello{Version: 1, Type: authentication.TypeHello, BodyID: bodyID, BodyNonce: dollnetwork.EncodeRawURL(nonce), Algorithms: []string{authentication.AlgorithmEd25519}}
}

func proof(t *testing.T, challenge authentication.Challenge, key ed25519.PrivateKey) authentication.Proof {
	t.Helper()
	value := authentication.Proof{Version: 1, Type: authentication.TypeProof, CoreID: challenge.CoreID, BodyID: challenge.BodyID, SessionID: challenge.SessionID, BodyNonce: challenge.BodyNonce, CoreNonce: challenge.CoreNonce, Algorithm: challenge.Algorithm}
	if err := authentication.SignProof(key, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAuthenticationServiceRejectsForgeryExpiryReplayAndRevocation(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	bodyID, privateKey := enrollBody(t, fx, "auth-body")
	wrongPublic, wrongPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil || wrongPublic == nil {
		t.Fatal(err)
	}

	if _, err := fx.service.BeginAuthentication(context.Background(), "forged", hello(t, "body_unknown")); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("forged BodyID error = %v", err)
	}
	challenge, err := fx.service.BeginAuthentication(context.Background(), "wrong-key", hello(t, bodyID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fx.service.CompleteAuthentication(context.Background(), "wrong-key", proof(t, challenge, wrongPrivate)); !errors.Is(err, enrollment.ErrInvalidProof) {
		t.Fatalf("wrong key error = %v", err)
	}

	challenge, err = fx.service.BeginAuthentication(context.Background(), "expired", hello(t, bodyID))
	if err != nil {
		t.Fatal(err)
	}
	fx.clock.now = challenge.ExpiresAt
	if _, _, err := fx.service.CompleteAuthentication(context.Background(), "expired", proof(t, challenge, privateKey)); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("expired proof error = %v", err)
	}
	fx.clock.now = challenge.ExpiresAt.Add(-time.Second)

	challenge, err = fx.service.BeginAuthentication(context.Background(), "replay", hello(t, bodyID))
	if err != nil {
		t.Fatal(err)
	}
	signedProof := proof(t, challenge, privateKey)
	if _, _, err := fx.service.CompleteAuthentication(context.Background(), "replay", signedProof); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fx.service.CompleteAuthentication(context.Background(), "replay", signedProof); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("replayed proof error = %v", err)
	}

	bodyRecord, err := fx.store.(enrollment.Store).GetBody(context.Background(), bodyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.network.RevokeMembership(network.PeerID(bodyRecord.MembershipRef)); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.service.BeginAuthentication(context.Background(), "revoked", hello(t, bodyID)); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("revoked membership error = %v", err)
	}
}

func TestAuthenticationReloadsDurableCredentialBeforeAccept(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB, string)
	}{
		{"missing", func(t *testing.T, db *sql.DB, bodyID string) {
			if _, err := db.Exec(`DELETE FROM body_credentials WHERE body_id = ?`, bodyID); err != nil {
				t.Fatal(err)
			}
		}},
		{"rotated", func(t *testing.T, db *sql.DB, bodyID string) {
			publicKey, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE body_credentials SET public_key = ?, key_version = key_version + 1 WHERE body_id = ?`, publicKey, bodyID); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced-membership", func(t *testing.T, db *sql.DB, bodyID string) {
			if _, err := db.Exec(`UPDATE body_credentials SET membership_reference = ? WHERE body_id = ?`, "replacement-membership", bodyID); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoked", func(t *testing.T, db *sql.DB, bodyID string) {
			if _, err := db.Exec(`UPDATE body_credentials SET revoked_at = ? WHERE body_id = ?`, time.Now().UTC().Format(time.RFC3339Nano), bodyID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "core.db")
			fx := newFixture(t, path)
			bodyID, privateKey := enrollBody(t, fx, "durable-"+test.name)
			challenge, err := fx.service.BeginAuthentication(context.Background(), "connection", hello(t, bodyID))
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			test.mutate(t, db, bodyID)
			if _, _, err := fx.service.CompleteAuthentication(context.Background(), "connection", proof(t, challenge, privateKey)); err == nil {
				t.Fatal("proof was accepted after durable credential changed")
			}
		})
	}
}

type authTestLogger struct{}

func (authTestLogger) Info(string, ...map[string]any)  {}
func (authTestLogger) Warn(string, ...map[string]any)  {}
func (authTestLogger) Error(string, ...map[string]any) {}

func startAuthServer(t *testing.T, fx *fixture, authenticationTimeout time.Duration, authenticator ws.BodyAuthenticator) (*ws.Server, string, *body.Registry) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	registry := body.NewRegistry()
	if authenticator == nil {
		authenticator = fx.service
	}
	server := ws.New(ws.Config{BodyAuthenticator: authenticator, AuthenticationTimeout: authenticationTimeout}, authTestLogger{}, nil)
	registrar, err := body.NewRemoteRegistrar(registry, fx.store.(network.NetworkStore), server)
	if err != nil {
		t.Fatal(err)
	}
	server.SetExecutionSessionLifecycle(registrar)
	server.SetListener(listener)
	done := make(chan error, 1)
	go func() { done <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return server, "ws://" + listener.Addr().String() + "/ws", registry
}

func authenticateClient(t *testing.T, url, bodyID string, key ed25519.PrivateKey, coreKey ed25519.PublicKey) (*websocket.Conn, authentication.Proof) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(hello(t, bodyID)); err != nil {
		t.Fatal(err)
	}
	var challenge authentication.Challenge
	if err := conn.ReadJSON(&challenge); err != nil {
		t.Fatal(err)
	}
	if err := authentication.VerifyChallenge(coreKey, challenge); err != nil {
		t.Fatal(err)
	}
	signedProof := proof(t, challenge, key)
	if err := conn.WriteJSON(signedProof); err != nil {
		t.Fatal(err)
	}
	var accept authentication.Accept
	if err := conn.ReadJSON(&accept); err != nil {
		t.Fatal(err)
	}
	if accept.BodyID != challenge.BodyID || accept.SessionID != challenge.SessionID || accept.Status != "authenticated" {
		t.Fatalf("invalid accept: %#v", accept)
	}
	if err := authentication.VerifyAccept(coreKey, accept); err != nil {
		t.Fatal(err)
	}
	return conn, signedProof
}

func TestAuthenticatedWebSocketRegistrationExecutionReplacementAndPreAuthRejection(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	bodyID, privateKey := enrollBody(t, fx, "ws-auth-body")
	server, url, registry := startAuthServer(t, fx, 0, nil)
	if _, err := server.Execute(context.Background(), bodyID, "doll", execution.RequestPayload{ExecutionID: "pre-auth", Capability: "echo", Operation: "run", Arguments: map[string]any{}}); !errors.Is(err, ws.ErrNoAuthenticatedSession) {
		t.Fatalf("pre-authentication execution error = %v", err)
	}

	unauthenticated, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	advertisement := capabilities.NewAdvertisement(bodyID, []capabilities.Capability{{ID: "echo", Operations: []string{"run"}, Available: true}})
	if err := unauthenticated.WriteJSON(advertisement); err != nil {
		t.Fatal(err)
	}
	unauthenticated.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := unauthenticated.ReadMessage(); err == nil {
		t.Fatal("pre-authentication capability traffic did not close connection")
	}
	if _, exists := registry.Get(body.BodyID(bodyID)); exists {
		t.Fatal("pre-authentication capability advertisement registered Body")
	}
	unauthenticated.Close()

	coreKey := fx.service.CoreIdentity().PublicKey
	first, replayedProof := authenticateClient(t, url, bodyID, privateKey, coreKey)
	if err := first.WriteJSON(advertisement); err != nil {
		t.Fatal(err)
	}
	second, _ := authenticateClient(t, url, bodyID, privateKey, coreKey)
	defer second.Close()
	if err := second.WriteJSON(advertisement); err != nil {
		t.Fatal(err)
	}

	bodyDone := make(chan error, 1)
	go func() {
		var request execution.Request
		if err := second.ReadJSON(&request); err != nil {
			bodyDone <- err
			return
		}
		bodyDone <- second.WriteJSON(execution.Result{Type: execution.TypeResult, ID: "result", Timestamp: time.Now().UTC(), DollID: request.DollID, BodyID: bodyID, CorrelationID: request.ID, Payload: execution.ResultPayload{ExecutionID: request.Payload.ExecutionID, Status: execution.StatusSuccess, Result: map[string]any{"ok": true}}})
	}()
	result, err := server.Execute(context.Background(), bodyID, "doll", execution.RequestPayload{ExecutionID: "exec", Capability: "echo", Operation: "run", Arguments: map[string]any{}})
	if err != nil || result.Payload.Status != execution.StatusSuccess {
		t.Fatalf("execute result = %#v, %v", result, err)
	}
	if err := <-bodyDone; err != nil {
		t.Fatal(err)
	}

	// A proof is single-use and is never accepted as ordinary post-auth traffic.
	if err := second.WriteJSON(replayedProof); err != nil {
		t.Fatal(err)
	}
	second.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := second.ReadMessage(); err == nil {
		t.Fatal("replayed proof did not close connection")
	}

	// The replaced generation cannot receive the request selected for the Body.
	first.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("replaced connection remained active")
	}
}

func TestAuthenticationTimeoutClosesIdleAndAbandonedHandshakes(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	bodyID, privateKey := enrollBody(t, fx, "timeout-body")
	recorder := &recordingAuthenticator{delegate: fx.service}
	_, url, _ := startAuthServer(t, fx, 50*time.Millisecond, recorder)

	idle, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	idle.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := idle.ReadMessage(); err == nil {
		t.Fatal("idle unauthenticated connection remained open")
	}
	idle.Close()

	abandoned, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := abandoned.WriteJSON(hello(t, bodyID)); err != nil {
		t.Fatal(err)
	}
	var challenge authentication.Challenge
	if err := abandoned.ReadJSON(&challenge); err != nil {
		t.Fatal(err)
	}
	abandoned.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := abandoned.ReadMessage(); err == nil {
		t.Fatal("abandoned challenge connection remained open")
	}
	abandoned.Close()
	if _, _, err := fx.service.CompleteAuthentication(context.Background(), recorder.connectionID(), proof(t, challenge, privateKey)); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("timed-out pending challenge was not cleaned up: %v", err)
	}

	// Timeout cleanup must allow a fresh connection for the same identity.
	reconnected, _ := authenticateClient(t, url, bodyID, privateKey, fx.service.CoreIdentity().PublicKey)
	reconnected.Close()
}

type recordingPeerRefresher struct {
	mu    sync.Mutex
	calls int
}

type blockingActivationAuthenticator struct {
	delegate  *enrollment.Service
	entered   chan struct{}
	release   chan struct{}
	activated chan struct{}
	once      sync.Once
}

func (a *blockingActivationAuthenticator) BeginAuthentication(ctx context.Context, connectionID string, value authentication.Hello) (authentication.Challenge, error) {
	return a.delegate.BeginAuthentication(ctx, connectionID, value)
}

func (a *blockingActivationAuthenticator) CompleteAuthentication(ctx context.Context, connectionID string, value authentication.Proof) (authentication.Accept, string, error) {
	return a.delegate.CompleteAuthentication(ctx, connectionID, value)
}

func (a *blockingActivationAuthenticator) ActivateAuthentication(ctx context.Context, connectionID string, value authentication.Proof, activate func(authentication.Accept, string) error) error {
	return a.delegate.ActivateAuthentication(ctx, connectionID, value, func(accept authentication.Accept, bodyID string) error {
		a.once.Do(func() { close(a.entered) })
		<-a.release
		err := activate(accept, bodyID)
		close(a.activated)
		return err
	})
}

func (a *blockingActivationAuthenticator) CancelAuthentication(connectionID string) {
	a.delegate.CancelAuthentication(connectionID)
}

type blockingRevokeAuthorizer struct {
	entered chan struct{}
	release chan struct{}
}

func (a *blockingRevokeAuthorizer) AuthorizeEnrollment(_ context.Context, _ string, action enrollment.OwnerAction) error {
	if action.Operation == "revoke" {
		close(a.entered)
		<-a.release
	}
	return nil
}

func TestAuthenticationActivationIsAtomicWithRevocation(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	bodyID, privateKey := enrollBody(t, fx, "activation-revocation-race")

	owner := &blockingRevokeAuthorizer{entered: make(chan struct{}), release: make(chan struct{})}
	service, err := enrollment.NewService(context.Background(), fx.store.(enrollment.Store), fx.network, fx.invitations, owner, fx.clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	fx.service = service
	authenticator := &blockingActivationAuthenticator{
		delegate: service, entered: make(chan struct{}), release: make(chan struct{}), activated: make(chan struct{}),
	}
	server, url, _ := startAuthServer(t, fx, 0, authenticator)
	service.SetRevocationLifecycle(server, nil)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(hello(t, bodyID)); err != nil {
		t.Fatal(err)
	}
	var challenge authentication.Challenge
	if err := conn.ReadJSON(&challenge); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(proof(t, challenge, privateKey)); err != nil {
		t.Fatal(err)
	}
	<-authenticator.entered

	revoked := make(chan error, 1)
	go func() { revoked <- service.Revoke(context.Background(), bodyID, "owner") }()
	select {
	case <-owner.entered:
		t.Fatal("revocation crossed the lifecycle boundary while activation was paused")
	default:
	}

	close(authenticator.release)
	<-authenticator.activated
	<-owner.entered // the exclusive lifecycle transition now owns the boundary
	close(owner.release)
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := server.Execute(context.Background(), bodyID, "doll", execution.RequestPayload{
		ExecutionID: "after-revocation", Capability: "test", Operation: "run", Arguments: map[string]any{},
	}); !errors.Is(err, ws.ErrNoAuthenticatedSession) {
		t.Fatalf("execution after revocation error = %v", err)
	}
}

func (r *recordingPeerRefresher) RefreshPeers(context.Context) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return nil
}

func TestRevocationDisconnectsActiveSessionCancelsExecutionAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.db")
	fx := newFixture(t, path)
	bodyID, privateKey := enrollBody(t, fx, "revoked-active")
	server, url, _ := startAuthServer(t, fx, 0, nil)
	peers := &recordingPeerRefresher{}
	fx.service.SetRevocationLifecycle(server, peers)
	conn, _ := authenticateClient(t, url, bodyID, privateKey, fx.service.CoreIdentity().PublicKey)
	defer conn.Close()

	received := make(chan struct{})
	go func() {
		var request execution.Request
		if conn.ReadJSON(&request) == nil {
			close(received)
		}
	}()
	executionDone := make(chan error, 1)
	go func() {
		_, err := server.Execute(context.Background(), bodyID, "doll", execution.RequestPayload{ExecutionID: "pending-revocation", Capability: "test", Operation: "run", Arguments: map[string]any{}})
		executionDone <- err
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("execution request was not delivered")
	}
	if err := fx.service.Revoke(context.Background(), bodyID, "owner"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-executionDone:
		if !errors.Is(err, execution.ErrDisconnected) {
			t.Fatalf("pending execution error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not release pending execution")
	}
	body, err := fx.store.(enrollment.Store).GetBody(context.Background(), bodyID)
	if err != nil || body.RevokedAt == nil {
		t.Fatalf("durable revoked Body = %#v, %v", body, err)
	}
	membership := fx.network.GetMembership(network.PeerID(body.MembershipRef))
	if membership == nil || membership.Status != network.MembershipRevoked {
		t.Fatalf("revoked membership = %#v", membership)
	}
	peers.mu.Lock()
	refreshes := peers.calls
	peers.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("peer refreshes = %d, want 1", refreshes)
	}
	if err := fx.service.Revoke(context.Background(), bodyID, "owner"); err != nil {
		t.Fatalf("idempotent revocation: %v", err)
	}
	peers.mu.Lock()
	refreshes = peers.calls
	peers.mu.Unlock()
	if refreshes != 2 {
		t.Fatalf("idempotent revocation did not reconcile peers: calls = %d", refreshes)
	}

	reconnect, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnect.Close()
	if err := reconnect.WriteJSON(hello(t, bodyID)); err != nil {
		t.Fatal(err)
	}
	reconnect.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := reconnect.ReadMessage(); err == nil {
		t.Fatal("revoked Body reauthenticated")
	}

	if err := fx.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restoredBody, err := reopened.(enrollment.Store).GetBody(context.Background(), bodyID)
	if err != nil || restoredBody.RevokedAt == nil {
		t.Fatalf("restart Body revocation = %#v, %v", restoredBody, err)
	}
	restoredMembership, err := reopened.(network.NetworkStore).LoadMembership(context.Background(), network.PeerID(body.MembershipRef))
	if err != nil || restoredMembership.Status != network.MembershipRevoked {
		t.Fatalf("restart membership = %#v, %v", restoredMembership, err)
	}
	restoredNetwork, err := reopened.(network.NetworkStore).LoadNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := enrollment.NewService(context.Background(), reopened.(enrollment.Store), restoredNetwork, invitation.NewService(reopened.(invitation.Store), fx.clock), enrollment.AllowOwnerAuthorizer{}, fx.clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.BeginAuthentication(context.Background(), "restart-reconnect", hello(t, bodyID)); !errors.Is(err, enrollment.ErrUnauthorized) {
		t.Fatalf("revoked Body authentication after restart error = %v", err)
	}
}

type recordingAuthenticator struct {
	delegate *enrollment.Service
	mu       sync.Mutex
	lastID   string
}

func (r *recordingAuthenticator) BeginAuthentication(ctx context.Context, connectionID string, value authentication.Hello) (authentication.Challenge, error) {
	r.mu.Lock()
	r.lastID = connectionID
	r.mu.Unlock()
	return r.delegate.BeginAuthentication(ctx, connectionID, value)
}

func (r *recordingAuthenticator) CompleteAuthentication(ctx context.Context, connectionID string, value authentication.Proof) (authentication.Accept, string, error) {
	return r.delegate.CompleteAuthentication(ctx, connectionID, value)
}

func (r *recordingAuthenticator) ActivateAuthentication(ctx context.Context, connectionID string, value authentication.Proof, activate func(authentication.Accept, string) error) error {
	return r.delegate.ActivateAuthentication(ctx, connectionID, value, activate)
}

func (r *recordingAuthenticator) CancelAuthentication(connectionID string) {
	r.delegate.CancelAuthentication(connectionID)
}

func (r *recordingAuthenticator) connectionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastID
}
