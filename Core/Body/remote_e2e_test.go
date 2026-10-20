package body

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

const remoteE2EToken = "trusted-e2e-fixture"

type remoteE2EHarness struct {
	guard  *Guard
	server *ws.Server
	client *ws.Client
	cancel context.CancelFunc
}

func newRemoteE2EHarness(t *testing.T, decision AuthorityDecision, permission execution.LocalPermission, executor execution.CapabilityExecutor) *remoteE2EHarness {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := ws.New(ws.Config{AuthenticateExecution: func(request *http.Request) (string, error) {
		if request.Header.Get("Authorization") != "Bearer "+remoteE2EToken {
			return "", errors.New("invalid trusted fixture token")
		}
		return string(remoteTestBodyID), nil
	}}, remoteTestLogger{}, nil)
	server.SetListener(listener)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(context.Background()) }()

	header := http.Header{"Authorization": []string{"Bearer " + remoteE2EToken}}
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/ws", header)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := execution.NewDispatcher(string(remoteTestBodyID), permission, executor)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ws.NewClient(conn, ws.ClientConfig{AuthenticatedExecutionHandler: dispatcher}, remoteTestLogger{})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, cancel := context.WithCancel(context.Background())
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Run(clientCtx) }()

	deadline := time.Now().Add(time.Second)
	for server.ConnCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("Body WebSocket did not connect")
		}
		time.Sleep(time.Millisecond)
	}

	remote, err := NewRemote(remoteTestBodyID, "remote e2e body", []Capability{{
		ID: "test", Operations: []string{"run", "unsupported", "fail", "block"}, Available: true,
	}}, server)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(remote); err != nil {
		t.Fatal(err)
	}
	rules := make([]AuthorityRule, 0, 4)
	for _, operation := range []string{"run", "unsupported", "fail", "block"} {
		rules = append(rules, AuthorityRule{
			Doll: "doll-1", Body: remoteTestBodyID, Capability: "test", Operation: operation, Decision: decision,
			Reason: "e2e_rule",
		})
	}
	guard := NewGuard(registry, NewRuleEvaluator(rules...))

	harness := &remoteE2EHarness{guard: guard, server: server, client: client, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-clientDone:
		case <-time.After(time.Second):
			t.Error("Body WebSocket client did not stop")
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Errorf("WebSocket server shutdown: %v", err)
		}
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("WebSocket server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("WebSocket server did not stop")
		}
	})
	return harness
}

func remoteE2ERequest(id, operation string, arguments map[string]any) ExecutionRequest {
	return ExecutionRequest{
		ExecutionID: id, Doll: "doll-1", Body: remoteTestBodyID,
		Capability: "test", Operation: operation, Arguments: arguments,
	}
}

func allowRemoteE2E(context.Context, execution.Request) bool { return true }

func TestRemoteExecutionEndToEndAuthorized(t *testing.T) {
	harness := newRemoteE2EHarness(t, AuthorityAllow, execution.LocalPermissionFunc(allowRemoteE2E), execution.CapabilityExecutorFunc(
		func(_ context.Context, capability, operation string, arguments map[string]any) (map[string]any, error) {
			return map[string]any{"capability": capability, "operation": operation, "value": arguments["value"]}, nil
		},
	))
	result := harness.guard.Execute(remoteE2ERequest("e2e-success", "run", map[string]any{"value": "ok"}))
	if result.Status != StatusSuccess || result.ExecutionID != "e2e-success" || result.Output != `{"capability":"test","operation":"run","value":"ok"}` {
		t.Fatalf("end-to-end result = %#v", result)
	}
}

func TestRemoteExecutionEndToEndGuardDenialDoesNotTransmit(t *testing.T) {
	var bodyPermissionChecks atomic.Int32
	var capabilityInvocations atomic.Int32
	harness := newRemoteE2EHarness(t, AuthorityDeny, execution.LocalPermissionFunc(func(context.Context, execution.Request) bool {
		bodyPermissionChecks.Add(1)
		return true
	}), execution.CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		capabilityInvocations.Add(1)
		return nil, nil
	}))
	result := harness.guard.Execute(remoteE2ERequest("core-denied", "run", nil))
	if result.Status != StatusDenied || result.ExecutionID != "core-denied" {
		t.Fatalf("Core denial = %#v", result)
	}
	if bodyPermissionChecks.Load() != 0 || capabilityInvocations.Load() != 0 {
		t.Fatalf("Core-denied request reached Body: permission checks = %d, invocations = %d", bodyPermissionChecks.Load(), capabilityInvocations.Load())
	}
}

func TestRemoteExecutionEndToEndBodyLocalDenial(t *testing.T) {
	var invoked atomic.Int32
	harness := newRemoteE2EHarness(t, AuthorityAllow, execution.LocalPermissionFunc(func(context.Context, execution.Request) bool {
		return false
	}), execution.CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		invoked.Add(1)
		return nil, nil
	}))
	result := harness.guard.Execute(remoteE2ERequest("body-denied", "run", nil))
	if result.Status != StatusDenied || result.ExecutionID != "body-denied" || invoked.Load() != 0 {
		t.Fatalf("Body denial = %#v, invocations = %d", result, invoked.Load())
	}
}

func TestRemoteExecutionEndToEndTerminalFailures(t *testing.T) {
	harness := newRemoteE2EHarness(t, AuthorityAllow, execution.LocalPermissionFunc(allowRemoteE2E), execution.CapabilityExecutorFunc(
		func(_ context.Context, _, operation string, _ map[string]any) (map[string]any, error) {
			switch operation {
			case "unsupported":
				return nil, execution.ErrUnsupported
			case "fail":
				return nil, errors.New("fixture failure")
			default:
				return nil, nil
			}
		},
	))
	for _, test := range []struct {
		operation string
		status    ExecutionStatus
	}{{"unsupported", StatusUnsupported}, {"fail", StatusFailed}} {
		result := harness.guard.Execute(remoteE2ERequest("e2e-"+test.operation, test.operation, nil))
		if result.Status != test.status || result.ExecutionID != "e2e-"+test.operation {
			t.Fatalf("%s result = %#v", test.operation, result)
		}
	}
}

func TestRemoteExecutionEndToEndConcurrentCorrelation(t *testing.T) {
	releases := map[string]chan struct{}{"first": make(chan struct{}), "second": make(chan struct{})}
	started := make(chan string, 2)
	harness := newRemoteE2EHarness(t, AuthorityAllow, execution.LocalPermissionFunc(allowRemoteE2E), execution.CapabilityExecutorFunc(
		func(ctx context.Context, _, _ string, arguments map[string]any) (map[string]any, error) {
			name := arguments["name"].(string)
			started <- name
			select {
			case <-releases[name]:
				return map[string]any{"name": name}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	))
	type outcome struct {
		name   string
		result ExecutionResult
	}
	results := make(chan outcome, 2)
	for _, name := range []string{"first", "second"} {
		go func(name string) {
			results <- outcome{name, harness.guard.Execute(remoteE2ERequest("execution-"+name, "run", map[string]any{"name": name}))}
		}(name)
	}
	<-started
	<-started
	close(releases["second"])
	second := <-results
	if second.name != "second" || second.result.ExecutionID != "execution-second" || second.result.Output != `{"name":"second"}` {
		t.Fatalf("second result = %#v", second)
	}
	close(releases["first"])
	first := <-results
	if first.name != "first" || first.result.ExecutionID != "execution-first" || first.result.Output != `{"name":"first"}` {
		t.Fatalf("first result = %#v", first)
	}
}

func TestRemoteExecutionEndToEndDisconnectCancelsWork(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var once sync.Once
	harness := newRemoteE2EHarness(t, AuthorityAllow, execution.LocalPermissionFunc(allowRemoteE2E), execution.CapabilityExecutorFunc(
		func(ctx context.Context, _, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			once.Do(func() { close(cancelled) })
			return nil, ctx.Err()
		},
	))
	resultDone := make(chan ExecutionResult, 1)
	go func() { resultDone <- harness.guard.Execute(remoteE2ERequest("disconnect", "block", nil)) }()
	<-started
	if err := harness.client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel Body-side work")
	}
	select {
	case result := <-resultDone:
		if result.Status != StatusFailed || result.ExecutionID != "disconnect" {
			t.Fatalf("disconnect result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not release pending Core execution")
	}
}

func TestRemoteExecutionEndToEndLeavesLocalBodyUnaffected(t *testing.T) {
	registry := NewRegistry()
	guard := NewGuard(registry, NewRuleEvaluator(AuthorityRule{
		Doll: "doll-1", Body: LocalBodyID, Capability: "runtime.info", Operation: "read", Decision: AuthorityAllow,
	}))
	result := guard.Execute(ExecutionRequest{
		ExecutionID: "local-e2e-regression", Doll: "doll-1", Body: LocalBodyID,
		Capability: "runtime.info", Operation: "read",
	})
	if result.Status != StatusSuccess || result.ExecutionID != "local-e2e-regression" {
		t.Fatalf("Local Body result = %#v", result)
	}
}
