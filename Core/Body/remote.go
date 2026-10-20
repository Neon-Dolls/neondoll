package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

// RemoteExecutionTransport is the targeted Doll Link execution boundary used
// by RemoteBody. DollLink/WebSocket.Server satisfies this interface and owns
// authenticated session selection, wire correlation, concurrent request
// tracking, and connection lifecycle cleanup.
type RemoteExecutionTransport interface {
	Execute(ctx context.Context, bodyID, dollID string, payload execution.RequestPayload) (execution.Result, error)
}

// RemoteBody adapts targeted Doll Link execution to Core's semantic Body
// boundary. It does not authenticate connections: bodyID only selects a
// session that the transport has independently authenticated. A fail-closed
// transport rejects execution when no such session exists.
type RemoteBody struct {
	id        BodyID
	name      string
	caps      *CapabilityRegistry
	transport RemoteExecutionTransport
}

var _ Body = (*RemoteBody)(nil)

// NewRemote creates a remote Body with its advertised capabilities. The Body
// identity passed here is a routing selector, never authentication proof.
func NewRemote(id BodyID, name string, capabilities []Capability, transport RemoteExecutionTransport) (*RemoteBody, error) {
	if id == "" {
		return nil, fmt.Errorf("remote body: Body identity is required")
	}
	if transport == nil {
		return nil, fmt.Errorf("remote body %q: execution transport is required", id)
	}

	body := &RemoteBody{id: id, name: name, caps: NewCapabilityRegistry(), transport: transport}
	for _, capability := range capabilities {
		if err := body.caps.Register(capability); err != nil {
			return nil, fmt.Errorf("remote body %q: %w", id, err)
		}
	}
	return body, nil
}

func (b *RemoteBody) ID() BodyID     { return b.id }
func (b *RemoteBody) Kind() BodyKind { return BodyKindRemote }
func (b *RemoteBody) Name() string   { return b.name }

func (b *RemoteBody) Describe() []Capability { return b.caps.All() }

func (b *RemoteBody) ResolveCapability(capID, operation string) error {
	return b.caps.Resolve(capID, operation)
}

func (b *RemoteBody) RegisterCapability(capability Capability) error {
	return b.caps.Register(capability)
}

// Execute satisfies Body using a non-cancellable background context. Core
// callers that have a request context use Guard.ExecuteContext; Guard.Execute
// remains compatible with the existing Body interface.
func (b *RemoteBody) Execute(request ExecutionRequest) (*ExecutionResult, error) {
	return b.ExecuteContext(context.Background(), request)
}

// ExecuteContext maps a resolved and authorized Core request to the canonical
// Body Protocol request payload and delegates correlation to the Doll Link
// session.
func (b *RemoteBody) ExecuteContext(ctx context.Context, request ExecutionRequest) (*ExecutionResult, error) {
	arguments := request.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	wireResult, err := b.transport.Execute(ctx, string(b.id), request.Doll, execution.RequestPayload{
		ExecutionID: request.ExecutionID,
		Capability:  request.Capability,
		Operation:   request.Operation,
		Arguments:   arguments,
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return &ExecutionResult{ExecutionID: request.ExecutionID, Status: StatusCancelled}, nil
		case errors.Is(err, context.DeadlineExceeded):
			return &ExecutionResult{ExecutionID: request.ExecutionID, Status: StatusExpired}, nil
		default:
			return nil, err
		}
	}

	status, err := coreExecutionStatus(wireResult.Payload.Status)
	if err != nil {
		return nil, err
	}
	var output string
	// Body Protocol v1 has no structured error-detail field. Preserve its
	// terminal status without synthesizing Core error codes or messages.
	if wireResult.Payload.Result != nil {
		encoded, err := json.Marshal(wireResult.Payload.Result)
		if err != nil {
			return nil, fmt.Errorf("remote body %q: encode execution result: %w", b.id, err)
		}
		output = string(encoded)
	}
	return &ExecutionResult{
		ExecutionID: wireResult.Payload.ExecutionID,
		Status:      status,
		Output:      output,
	}, nil
}

func coreExecutionStatus(status execution.Status) (ExecutionStatus, error) {
	switch status {
	case execution.StatusSuccess:
		return StatusSuccess, nil
	case execution.StatusDenied:
		return StatusDenied, nil
	case execution.StatusUnsupported:
		return StatusUnsupported, nil
	case execution.StatusInvalid:
		return StatusInvalid, nil
	case execution.StatusUnavailable:
		return StatusUnavailable, nil
	case execution.StatusFailed:
		return StatusFailed, nil
	case execution.StatusCancelled:
		return StatusCancelled, nil
	case execution.StatusExpired:
		return StatusExpired, nil
	default:
		return "", fmt.Errorf("remote body: unsupported execution status %q", status)
	}
}
