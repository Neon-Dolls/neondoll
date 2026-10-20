package body

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

const remoteTestBodyID BodyID = "remote::body-1"

var _ RemoteExecutionTransport = (*ws.Server)(nil)

type remoteTestLogger struct{}

func (remoteTestLogger) Info(string, ...map[string]any)  {}
func (remoteTestLogger) Warn(string, ...map[string]any)  {}
func (remoteTestLogger) Error(string, ...map[string]any) {}

type remoteRequestSender struct {
	requests chan execution.Request
}

func (s *remoteRequestSender) SendExecutionRequest(_ context.Context, request execution.Request) error {
	s.requests <- request
	return nil
}

type remoteSessionTransport struct {
	session *execution.Client
}

func (t remoteSessionTransport) Execute(ctx context.Context, bodyID, dollID string, payload execution.RequestPayload) (execution.Result, error) {
	if bodyID != t.session.BodyID() {
		return execution.Result{}, errors.New("session identity mismatch")
	}
	return t.session.Execute(ctx, dollID, payload)
}

func newRemoteGuard(t *testing.T, decision AuthorityDecision) (*Guard, *execution.Client, <-chan execution.Request) {
	t.Helper()
	sender := &remoteRequestSender{requests: make(chan execution.Request, 10)}
	session, err := execution.NewClient(string(remoteTestBodyID), sender)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewRemote(remoteTestBodyID, "remote test body", []Capability{{
		ID: "lights", Operations: []string{"set"}, Available: true,
	}}, remoteSessionTransport{session: session})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(remote); err != nil {
		t.Fatal(err)
	}
	guard := NewGuard(registry, NewRuleEvaluator(AuthorityRule{
		Doll: "doll-1", Body: remoteTestBodyID, Capability: "lights", Operation: "set", Decision: decision,
		Reason: "test_rule",
	}))
	return guard, session, sender.requests
}

func remoteTestRequest(id string) ExecutionRequest {
	return ExecutionRequest{
		ExecutionID: id,
		Doll:        "doll-1",
		Body:        remoteTestBodyID,
		Capability:  "lights",
		Operation:   "set",
		Arguments:   map[string]any{"state": "on"},
	}
}

func remoteResult(request execution.Request, status execution.Status, result map[string]any) execution.Result {
	return execution.Result{
		Type:          execution.TypeResult,
		ID:            "result-" + request.Payload.ExecutionID,
		Timestamp:     time.Now().UTC(),
		DollID:        request.DollID,
		BodyID:        request.BodyID,
		CorrelationID: request.ID,
		Payload: execution.ResultPayload{
			ExecutionID: request.Payload.ExecutionID,
			Status:      status,
			Result:      result,
		},
	}
}

func TestRemoteBodyAuthorizedGuardExecution(t *testing.T) {
	guard, session, requests := newRemoteGuard(t, AuthorityAllow)
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.Execute(remoteTestRequest("exec-1")) }()

	wireRequest := <-requests
	if wireRequest.BodyID != string(remoteTestBodyID) || wireRequest.DollID != "doll-1" {
		t.Fatalf("unexpected wire identity: %#v", wireRequest)
	}
	if wireRequest.Payload.ExecutionID != "exec-1" || wireRequest.Payload.Capability != "lights" || wireRequest.Payload.Operation != "set" {
		t.Fatalf("unexpected wire payload: %#v", wireRequest.Payload)
	}
	if err := session.HandleResult(remoteResult(wireRequest, execution.StatusSuccess, map[string]any{"state": "on"})); err != nil {
		t.Fatal(err)
	}

	result := <-done
	if result.ExecutionID != "exec-1" || result.Status != StatusSuccess || result.Output != `{"state":"on"}` {
		t.Fatalf("unexpected Core result: %#v", result)
	}
}

func TestRemoteBodyDeniedGuardExecutionDoesNotSend(t *testing.T) {
	guard, _, requests := newRemoteGuard(t, AuthorityDeny)
	result := guard.Execute(remoteTestRequest("denied"))
	if result.Status != StatusDenied {
		t.Fatalf("status = %q, want denied", result.Status)
	}
	select {
	case request := <-requests:
		t.Fatalf("denied execution sent remote request: %#v", request)
	default:
	}
}

func TestRemoteBodyTerminalFailurePropagates(t *testing.T) {
	guard, session, requests := newRemoteGuard(t, AuthorityAllow)
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.Execute(remoteTestRequest("failed")) }()
	wireRequest := <-requests
	if err := session.HandleResult(remoteResult(wireRequest, execution.StatusFailed, nil)); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Status != StatusFailed || result.ErrorCode != "" {
		t.Fatalf("remote failure result = %#v", result)
	}
}

func TestRemoteBodyDisconnectPropagates(t *testing.T) {
	guard, session, requests := newRemoteGuard(t, AuthorityAllow)
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.Execute(remoteTestRequest("disconnect")) }()
	<-requests
	session.Close(execution.ErrDisconnected)

	result := <-done
	if result.Status != StatusFailed || result.ErrorCode != "invocation_failed" || !strings.Contains(result.Error, execution.ErrDisconnected.Error()) {
		t.Fatalf("disconnect result = %#v", result)
	}
}

func TestRemoteBodyCancellationPropagatesThroughGuard(t *testing.T) {
	guard, _, requests := newRemoteGuard(t, AuthorityAllow)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.ExecuteContext(ctx, remoteTestRequest("cancel")) }()
	<-requests
	cancel()

	result := <-done
	if result.Status != StatusCancelled || result.ExecutionID != "cancel" {
		t.Fatalf("cancel result = %#v", result)
	}
}

func TestRemoteBodyMapsEveryRemoteTerminalStatus(t *testing.T) {
	tests := []struct {
		wire execution.Status
		core ExecutionStatus
	}{
		{execution.StatusDenied, StatusDenied},
		{execution.StatusUnsupported, StatusUnsupported},
		{execution.StatusInvalid, StatusInvalid},
		{execution.StatusUnavailable, StatusUnavailable},
		{execution.StatusCancelled, StatusCancelled},
		{execution.StatusExpired, StatusExpired},
	}
	for _, test := range tests {
		t.Run(string(test.wire), func(t *testing.T) {
			guard, session, requests := newRemoteGuard(t, AuthorityAllow)
			done := make(chan ExecutionResult, 1)
			go func() { done <- guard.Execute(remoteTestRequest(string(test.wire))) }()
			request := <-requests
			if err := session.HandleResult(remoteResult(request, test.wire, nil)); err != nil {
				t.Fatal(err)
			}
			if result := <-done; result.Status != test.core {
				t.Fatalf("status = %q, want %q", result.Status, test.core)
			}
		})
	}
}

func TestGuardExecuteLocalBodyUnchanged(t *testing.T) {
	registry := NewRegistry()
	guard := NewGuard(registry, NewRuleEvaluator(AuthorityRule{
		Doll: "doll-1", Body: LocalBodyID, Capability: "runtime.info", Operation: "read", Decision: AuthorityAllow,
	}))
	result := guard.Execute(ExecutionRequest{
		ExecutionID: "local", Doll: "doll-1", Body: LocalBodyID, Capability: "runtime.info", Operation: "read",
	})
	if result.Status != StatusSuccess || result.ExecutionID != "local" {
		t.Fatalf("local result = %#v", result)
	}
}

func TestRemoteBodyDeadlinePropagatesThroughGuard(t *testing.T) {
	guard, _, requests := newRemoteGuard(t, AuthorityAllow)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.ExecuteContext(ctx, remoteTestRequest("expired")) }()
	<-requests
	result := <-done
	if result.Status != StatusExpired {
		t.Fatalf("deadline result = %#v", result)
	}
}

func TestRemoteBodySessionErrorIsNotAProtocolStatus(t *testing.T) {
	guard, session, requests := newRemoteGuard(t, AuthorityAllow)
	done := make(chan ExecutionResult, 1)
	go func() { done <- guard.Execute(remoteTestRequest("closed")) }()
	<-requests
	session.Close(errors.New("transport failed"))
	if result := <-done; result.Status != StatusFailed || result.ErrorCode != "invocation_failed" {
		t.Fatalf("transport error result = %#v", result)
	}
}

func TestRemoteBodyWebSocketTransportFailsClosedWithoutAuthenticatedSession(t *testing.T) {
	transport := ws.New(ws.Config{}, remoteTestLogger{}, nil)
	remote, err := NewRemote(remoteTestBodyID, "remote", []Capability{{
		ID: "lights", Operations: []string{"set"}, Available: true,
	}}, transport)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(remote); err != nil {
		t.Fatal(err)
	}
	guard := NewGuard(registry, NewRuleEvaluator(AuthorityRule{
		Doll: "doll-1", Body: remoteTestBodyID, Capability: "lights", Operation: "set", Decision: AuthorityAllow,
	}))

	result := guard.Execute(remoteTestRequest("unauthenticated"))
	if result.Status != StatusFailed || result.ErrorCode != "invocation_failed" || !strings.Contains(result.Error, ws.ErrNoAuthenticatedSession.Error()) {
		t.Fatalf("unauthenticated result = %#v", result)
	}
}
