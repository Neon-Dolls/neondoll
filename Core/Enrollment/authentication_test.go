package enrollment_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Network"
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

type authTestLogger struct{}

func (authTestLogger) Info(string, ...map[string]any)  {}
func (authTestLogger) Warn(string, ...map[string]any)  {}
func (authTestLogger) Error(string, ...map[string]any) {}

func startAuthServer(t *testing.T, fx *fixture) (*ws.Server, string, *body.Registry) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	registry := body.NewRegistry()
	server := ws.New(ws.Config{BodyAuthenticator: fx.service}, authTestLogger{}, nil)
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
	server, url, registry := startAuthServer(t, fx)
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
