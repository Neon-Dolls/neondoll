package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func dispatcherRequest() Request {
	return Request{
		Type:      TypeRequest,
		ID:        "request-1",
		Timestamp: time.Now().UTC(),
		DollID:    "doll-1",
		BodyID:    "body-1",
		Payload: RequestPayload{
			ExecutionID: "execution-1",
			Capability:  "lights",
			Operation:   "set",
			Arguments:   map[string]any{"state": "on"},
		},
	}
}

func TestDispatcherDeniedPermissionNeverInvokes(t *testing.T) {
	var invoked atomic.Int32
	dispatcher, err := NewDispatcher("body-1", LocalPermissionFunc(func(context.Context, Request) bool {
		return false
	}), CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		invoked.Add(1)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.HandleExecutionRequest(context.Background(), dispatcherRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.Status != StatusDenied || invoked.Load() != 0 {
		t.Fatalf("result = %#v, invocations = %d", result, invoked.Load())
	}
}

func TestDispatcherCorrelatesTerminalStatuses(t *testing.T) {
	tests := []struct {
		name    string
		execErr error
		status  Status
	}{
		{"unsupported", ErrUnsupported, StatusUnsupported},
		{"invalid", ErrInvalid, StatusInvalid},
		{"unavailable", ErrUnavailable, StatusUnavailable},
		{"failed", errors.New("capability failed"), StatusFailed},
		{"cancelled", context.Canceled, StatusCancelled},
		{"expired", context.DeadlineExceeded, StatusExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher, err := NewDispatcher("body-1", LocalPermissionFunc(func(context.Context, Request) bool {
				return true
			}), CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
				return nil, test.execErr
			}))
			if err != nil {
				t.Fatal(err)
			}
			request := dispatcherRequest()
			result, err := dispatcher.HandleExecutionRequest(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.CorrelationID != request.ID || result.Payload.ExecutionID != request.Payload.ExecutionID || result.Payload.Status != test.status {
				t.Fatalf("uncorrelated result: %#v", result)
			}
			if err := result.Validate(SenderBody); err != nil {
				t.Fatal(err)
			}
			if err := result.ValidateResponseTo(request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDispatcherMalformedRequest(t *testing.T) {
	var invoked atomic.Int32
	dispatcher, err := NewDispatcher("body-1", LocalPermissionFunc(func(context.Context, Request) bool {
		return true
	}), CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		invoked.Add(1)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	correlatable := dispatcherRequest()
	correlatable.Payload.Operation = ""
	result, err := dispatcher.HandleExecutionRequest(context.Background(), correlatable)
	if err != nil || result.Payload.Status != StatusInvalid {
		t.Fatalf("correlatable malformed result = %#v, err = %v", result, err)
	}

	uncorrelatable := dispatcherRequest()
	uncorrelatable.ID = ""
	if _, err := dispatcher.HandleExecutionRequest(context.Background(), uncorrelatable); err == nil {
		t.Fatal("uncorrelatable malformed request was accepted")
	}
	wrongBody := dispatcherRequest()
	wrongBody.BodyID = "body-2"
	if _, err := dispatcher.HandleExecutionRequest(context.Background(), wrongBody); err == nil {
		t.Fatal("request for another Body was accepted")
	}
	if invoked.Load() != 0 {
		t.Fatalf("malformed requests invoked capability %d times", invoked.Load())
	}
}

func TestDispatcherNilPermissionFailsClosed(t *testing.T) {
	dispatcher, err := NewDispatcher("body-1", nil, CapabilityExecutorFunc(func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return map[string]any{"unsafe": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.HandleExecutionRequest(context.Background(), dispatcherRequest())
	if err != nil || result.Payload.Status != StatusDenied {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}
