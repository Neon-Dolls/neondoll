package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrUnsupported is returned by a local executor when it does not
	// implement the requested capability or operation.
	ErrUnsupported = errors.New("execution: unsupported local capability or operation")
	// ErrInvalid is returned by a local executor when arguments are malformed.
	ErrInvalid = errors.New("execution: invalid local execution request")
	// ErrUnavailable is returned when a supported local capability cannot
	// currently execute.
	ErrUnavailable = errors.New("execution: local capability unavailable")
)

// LocalPermission independently decides whether the Body permits a request.
// Permission must fail closed; capability availability and authenticated Core
// transport do not imply local permission.
type LocalPermission interface {
	AllowExecution(context.Context, Request) bool
}

// LocalPermissionFunc adapts a function to LocalPermission.
type LocalPermissionFunc func(context.Context, Request) bool

func (f LocalPermissionFunc) AllowExecution(ctx context.Context, request Request) bool {
	return f(ctx, request)
}

// CapabilityExecutor invokes one Body-local capability. Implementations must
// honor context cancellation where the underlying capability permits it.
type CapabilityExecutor interface {
	ExecuteCapability(context.Context, string, string, map[string]any) (map[string]any, error)
}

// CapabilityExecutorFunc adapts a function to CapabilityExecutor.
type CapabilityExecutorFunc func(context.Context, string, string, map[string]any) (map[string]any, error)

func (f CapabilityExecutorFunc) ExecuteCapability(ctx context.Context, capability, operation string, arguments map[string]any) (map[string]any, error) {
	return f(ctx, capability, operation, arguments)
}

// Dispatcher validates canonical requests, enforces independent Body-local
// permission, invokes a local capability, and builds correlated results.
type Dispatcher struct {
	bodyID     string
	permission LocalPermission
	executor   CapabilityExecutor
}

// NewDispatcher binds a dispatcher to the stable local Body identity.
func NewDispatcher(bodyID string, permission LocalPermission, executor CapabilityExecutor) (*Dispatcher, error) {
	if bodyID == "" {
		return nil, fmt.Errorf("execution: dispatcher Body identity is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("execution: local capability executor is required")
	}
	return &Dispatcher{bodyID: bodyID, permission: permission, executor: executor}, nil
}

// HandleExecutionRequest returns one terminal result for a valid or safely
// correlatable request. An envelope missing correlation-critical identities,
// or addressed to another Body, is rejected with an error and never invoked.
func (d *Dispatcher) HandleExecutionRequest(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.ID == "" || request.BodyID == "" || request.Payload.ExecutionID == "" {
		return Result{}, fmt.Errorf("execution: cannot correlate malformed request")
	}
	if request.BodyID != d.bodyID {
		return Result{}, fmt.Errorf("execution: request Body %q does not match local Body %q", request.BodyID, d.bodyID)
	}
	if err := request.Validate(SenderCore); err != nil {
		return d.result(request, StatusInvalid, nil), nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return d.result(request, StatusExpired, nil), nil
		}
		return d.result(request, StatusCancelled, nil), nil
	}
	if d.permission == nil || !d.permission.AllowExecution(ctx, request) {
		return d.result(request, StatusDenied, nil), nil
	}

	result, err := d.executor.ExecuteCapability(ctx, request.Payload.Capability, request.Payload.Operation, request.Payload.Arguments)
	if err == nil {
		return d.result(request, StatusSuccess, result), nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return d.result(request, StatusCancelled, nil), nil
	case errors.Is(err, context.DeadlineExceeded):
		return d.result(request, StatusExpired, nil), nil
	case errors.Is(err, ErrUnsupported):
		return d.result(request, StatusUnsupported, nil), nil
	case errors.Is(err, ErrInvalid):
		return d.result(request, StatusInvalid, nil), nil
	case errors.Is(err, ErrUnavailable):
		return d.result(request, StatusUnavailable, nil), nil
	default:
		return d.result(request, StatusFailed, nil), nil
	}
}

func (d *Dispatcher) result(request Request, status Status, result map[string]any) Result {
	return Result{
		Type:          TypeResult,
		ID:            uuid.NewString(),
		Timestamp:     time.Now().UTC(),
		DollID:        request.DollID,
		BodyID:        d.bodyID,
		CorrelationID: request.ID,
		Payload: ResultPayload{
			ExecutionID: request.Payload.ExecutionID,
			Status:      status,
			Result:      result,
		},
	}
}
