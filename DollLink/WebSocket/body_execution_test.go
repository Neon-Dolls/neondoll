package ws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

func startBodyExecutionClient(t *testing.T, url string, dispatcher *execution.Dispatcher, receiver EventReceiver) (*Client, context.CancelFunc) {
	return startBodyExecutionClientWithLimit(t, url, dispatcher, receiver, 0)
}

func startBodyExecutionClientWithLimit(t *testing.T, url string, dispatcher *execution.Dispatcher, receiver EventReceiver, maxConcurrent int) (*Client, context.CancelFunc) {
	t.Helper()
	conn := dialTestBody(t, url, "fixture-token")
	config := ClientConfig{EventReceiver: receiver, MaxConcurrentExecutions: maxConcurrent}
	if dispatcher != nil {
		config.AuthenticatedExecutionHandler = dispatcher
	}
	client, err := NewClient(conn, config, &testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Body WebSocket client did not stop")
		}
	})
	return client, cancel
}

func TestBodyExecutionSaturationReturnsUnavailableWithoutQueuing(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var invoked atomic.Int32
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(ctx context.Context, _, _ string, _ map[string]any) (map[string]any, error) {
			invoked.Add(1)
			close(started)
			select {
			case <-release:
				return map[string]any{"done": true}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	startBodyExecutionClientWithLimit(t, url, dispatcher, nil, 1)
	waitForConnection(t, server, true)

	firstDone := make(chan error, 1)
	go func() {
		_, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
			ExecutionID: "first", Capability: "test", Operation: "run", Arguments: map[string]any{},
		})
		firstDone <- err
	}()
	<-started

	secondDone := make(chan struct {
		result execution.Result
		err    error
	}, 1)
	go func() {
		result, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
			ExecutionID: "second", Capability: "test", Operation: "run", Arguments: map[string]any{},
		})
		secondDone <- struct {
			result execution.Result
			err    error
		}{result, err}
	}()
	select {
	case second := <-secondDone:
		if second.err != nil {
			t.Fatal(second.err)
		}
		if second.result.Payload.ExecutionID != "second" || second.result.Payload.Status != execution.StatusUnavailable {
			t.Fatalf("saturation result = %#v", second.result)
		}
	case <-time.After(time.Second):
		t.Fatal("saturated request queued instead of returning unavailable")
	}
	if invoked.Load() != 1 {
		t.Fatalf("capability invoked %d times, want 1", invoked.Load())
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestBodyExecutionCompletedRequestReleasesConcurrencySlot(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(_ context.Context, _, _ string, _ map[string]any) (map[string]any, error) {
			return map[string]any{"done": true}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	startBodyExecutionClientWithLimit(t, url, dispatcher, nil, 1)
	waitForConnection(t, server, true)

	for i := 0; i < 100; i++ {
		result, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
			ExecutionID: fmt.Sprintf("sequential-%d", i), Capability: "test", Operation: "run", Arguments: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Payload.Status != execution.StatusSuccess {
			t.Fatalf("completed request %d left slot saturated: %#v", i, result)
		}
	}
}

func allowAllLocalExecution() execution.LocalPermission {
	return execution.LocalPermissionFunc(func(context.Context, execution.Request) bool { return true })
}

func TestBodyExecutionRoundTripOverRealWebSocket(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(_ context.Context, capability, operation string, arguments map[string]any) (map[string]any, error) {
			if capability != "lights" || operation != "set" {
				return nil, execution.ErrUnsupported
			}
			return map[string]any{"state": arguments["state"]}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	startBodyExecutionClient(t, url, dispatcher, nil)
	waitForConnection(t, server, true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := server.Execute(ctx, "body-1", "doll-1", execution.RequestPayload{
		ExecutionID: "execution-1", Capability: "lights", Operation: "set", Arguments: map[string]any{"state": "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.Status != execution.StatusSuccess || result.Payload.ExecutionID != "execution-1" || result.Payload.Result["state"] != "on" {
		t.Fatalf("unexpected execution result: %#v", result)
	}
}

func TestBodyExecutionDeniedLocallyNeverInvokes(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	var invoked atomic.Int32
	dispatcher, err := execution.NewDispatcher("body-1", execution.LocalPermissionFunc(
		func(context.Context, execution.Request) bool { return false },
	), execution.CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		invoked.Add(1)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	startBodyExecutionClient(t, url, dispatcher, nil)
	waitForConnection(t, server, true)

	result, err := server.Execute(context.Background(), "body-1", "doll-1", execution.RequestPayload{
		ExecutionID: "denied", Capability: "lights", Operation: "set", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.Status != execution.StatusDenied || invoked.Load() != 0 {
		t.Fatalf("result = %#v, invocations = %d", result, invoked.Load())
	}
}

func TestBodyExecutionUnsupportedAndFailed(t *testing.T) {
	tests := []struct {
		name    string
		execErr error
		status  execution.Status
	}{
		{"unsupported", execution.ErrUnsupported, execution.StatusUnsupported},
		{"failed", errors.New("device failure"), execution.StatusFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
			dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
				func(context.Context, string, string, map[string]any) (map[string]any, error) {
					return nil, test.execErr
				},
			))
			if err != nil {
				t.Fatal(err)
			}
			startBodyExecutionClient(t, url, dispatcher, nil)
			waitForConnection(t, server, true)
			result, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
				ExecutionID: test.name, Capability: "test", Operation: "run", Arguments: map[string]any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Payload.Status != test.status {
				t.Fatalf("status = %q, want %q", result.Payload.Status, test.status)
			}
		})
	}
}

func TestBodyExecutionConcurrentRequestsRemainCorrelated(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	releaseFirst := make(chan struct{})
	var release sync.Once
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(ctx context.Context, _, _ string, arguments map[string]any) (map[string]any, error) {
			order := arguments["order"].(float64)
			if order == 1 {
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else {
				release.Do(func() { close(releaseFirst) })
			}
			return map[string]any{"order": order}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	startBodyExecutionClient(t, url, dispatcher, nil)
	waitForConnection(t, server, true)

	type outcome struct {
		result execution.Result
		err    error
	}
	outcomes := make(chan outcome, 2)
	for i := 1; i <= 2; i++ {
		i := i
		go func() {
			result, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
				ExecutionID: string(rune('0' + i)), Capability: "test", Operation: "run", Arguments: map[string]any{"order": i},
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		id := outcome.result.Payload.ExecutionID
		order := outcome.result.Payload.Result["order"].(float64)
		if (id == "1" && order != 1) || (id == "2" && order != 2) {
			t.Fatalf("mismatched result: %#v", outcome.result)
		}
		seen[id] = true
	}
	if !seen["1"] || !seen["2"] {
		t.Fatalf("missing results: %v", seen)
	}
}

func TestBodyExecutionDisconnectCancelsHandler(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(ctx context.Context, _, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	client, _ := startBodyExecutionClient(t, url, dispatcher, nil)
	waitForConnection(t, server, true)
	done := make(chan error, 1)
	go func() {
		_, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
			ExecutionID: "disconnect", Capability: "test", Operation: "run", Arguments: map[string]any{},
		})
		done <- err
	}()
	<-started
	_ = client.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel Body capability")
	}
	select {
	case err := <-done:
		if !errors.Is(err, execution.ErrDisconnected) {
			t.Fatalf("Core execution error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not release Core request")
	}
}

func TestBodyExecutionClientPreservesEventsAndActions(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, echoEventHandler{})
	received := make(chan events.Event, 2)
	receiver := EventReceiverFunc(func(_ context.Context, event *events.Event) error {
		received <- *event
		return nil
	})
	dispatcher, err := execution.NewDispatcher("body-1", allowAllLocalExecution(), execution.CapabilityExecutorFunc(
		func(context.Context, string, string, map[string]any) (map[string]any, error) {
			return nil, execution.ErrUnsupported
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	client, _ := startBodyExecutionClient(t, url, dispatcher, receiver)
	waitForConnection(t, server, true)

	event := events.NewDollMessage("event-1", "doll-1", "hello")
	if err := client.SendEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-received:
		if response.CorrelationID != event.ID {
			t.Fatalf("event correlation = %q", response.CorrelationID)
		}
	case <-time.After(time.Second):
		t.Fatal("Body client did not receive existing Event response")
	}

	if err := server.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage, Payload: actions.SendMessagePayload{Text: "still works"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		if event.Type != events.TypeMessage {
			t.Fatalf("event type = %q", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("Body client did not receive existing Action event")
	}
}

func TestBodyExecutionClientFailsClosedWithoutTrustedHandler(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	startBodyExecutionClient(t, url, nil, nil)
	waitForConnection(t, server, true)

	executeCtx, cancelExecute := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelExecute()
	_, err := server.Execute(executeCtx, "body-1", "", execution.RequestPayload{
		ExecutionID: "blocked", Capability: "test", Operation: "run", Arguments: map[string]any{},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want deadline exceeded", err)
	}
}
