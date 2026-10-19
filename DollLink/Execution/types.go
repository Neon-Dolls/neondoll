// Package execution defines the Body Protocol v1 execution wire messages
// carried by Doll Link. These types intentionally remain separate from
// Core/Body's in-process execution types.
package execution

import (
	"fmt"
	"time"
)

// Type is a canonical Body Protocol message type.
type Type string

const (
	TypeRequest Type = "execution.request"
	TypeResult  Type = "execution.result"
)

// Sender identifies the authenticated side that sent a message.
type Sender string

const (
	SenderCore Sender = "core"
	SenderBody Sender = "body"
)

// Status is a terminal execution outcome from Body Protocol v1.
type Status string

const (
	StatusSuccess     Status = "success"
	StatusDenied      Status = "denied"
	StatusUnsupported Status = "unsupported"
	StatusInvalid     Status = "invalid"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusExpired     Status = "expired"
)

// Request is the canonical Doll Link envelope for execution.request.
type Request struct {
	Type          Type           `json:"type"`
	ID            string         `json:"id"`
	Timestamp     time.Time      `json:"timestamp"`
	DollID        string         `json:"doll_id,omitempty"`
	BodyID        string         `json:"body_id"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Payload       RequestPayload `json:"payload"`
}

// RequestPayload identifies one Body capability operation. Arguments remain a
// JSON object whose semantics belong to the named capability and operation.
type RequestPayload struct {
	ExecutionID string         `json:"execution_id"`
	Capability  string         `json:"capability"`
	Operation   string         `json:"operation"`
	Arguments   map[string]any `json:"arguments"`
}

// Result is the canonical Doll Link envelope for execution.result.
type Result struct {
	Type          Type          `json:"type"`
	ID            string        `json:"id"`
	Timestamp     time.Time     `json:"timestamp"`
	DollID        string        `json:"doll_id,omitempty"`
	BodyID        string        `json:"body_id"`
	CorrelationID string        `json:"correlation_id"`
	Payload       ResultPayload `json:"payload"`
}

// ResultPayload is a terminal execution outcome. Result remains a JSON object
// whose semantics belong to the capability operation.
//
// Body Protocol v1 does not define a wire field or shape for the structured
// error details required semantically by body-contract.md. Do not infer one
// here; that specification gap must be resolved before error details are added.
type ResultPayload struct {
	ExecutionID string         `json:"execution_id"`
	Status      Status         `json:"status"`
	Result      map[string]any `json:"result,omitempty"`
}

// Validate verifies an execution request and its canonical sender direction.
// Doll Link version negotiation happens before these messages are exchanged;
// these passive wire types do not advertise a supported version range.
func (r Request) Validate(sender Sender) error {
	if sender != SenderCore {
		return fmt.Errorf("execution: execution.request must be sent by Core")
	}
	if r.Type != TypeRequest {
		return fmt.Errorf("execution: request type %q, want %q", r.Type, TypeRequest)
	}
	if err := validateEnvelope(r.ID, r.Timestamp, r.BodyID); err != nil {
		return err
	}
	if r.CorrelationID != "" {
		return fmt.Errorf("execution: execution.request must not have correlation_id")
	}
	if r.Payload.ExecutionID == "" || r.Payload.Capability == "" || r.Payload.Operation == "" {
		return fmt.Errorf("execution: request payload requires execution_id, capability, and operation")
	}
	if r.Payload.Arguments == nil {
		return fmt.Errorf("execution: request arguments must be a JSON object")
	}
	return nil
}

// Validate verifies an execution result and its canonical sender direction.
// Doll Link version negotiation happens before these messages are exchanged;
// these passive wire types do not advertise a supported version range.
func (r Result) Validate(sender Sender) error {
	if sender != SenderBody {
		return fmt.Errorf("execution: execution.result must be sent by Body")
	}
	if r.Type != TypeResult {
		return fmt.Errorf("execution: result type %q, want %q", r.Type, TypeResult)
	}
	if err := validateEnvelope(r.ID, r.Timestamp, r.BodyID); err != nil {
		return err
	}
	if r.CorrelationID == "" {
		return fmt.Errorf("execution: execution.result requires correlation_id")
	}
	if r.Payload.ExecutionID == "" {
		return fmt.Errorf("execution: result payload requires execution_id")
	}
	if !r.Payload.Status.valid() {
		return fmt.Errorf("execution: unknown terminal status %q", r.Payload.Status)
	}
	return nil
}

// ValidateResponseTo verifies the direct message correlation and stable
// execution/body identities required between a request and its result.
func (r Result) ValidateResponseTo(request Request) error {
	if r.CorrelationID != request.ID {
		return fmt.Errorf("execution: result correlation_id %q, want request id %q", r.CorrelationID, request.ID)
	}
	if r.Payload.ExecutionID != request.Payload.ExecutionID {
		return fmt.Errorf("execution: result execution_id %q, want %q", r.Payload.ExecutionID, request.Payload.ExecutionID)
	}
	if r.BodyID != request.BodyID {
		return fmt.Errorf("execution: result body_id %q, want %q", r.BodyID, request.BodyID)
	}
	if request.DollID != "" && r.DollID != request.DollID {
		return fmt.Errorf("execution: result doll_id %q, want %q", r.DollID, request.DollID)
	}
	return nil
}

func validateEnvelope(id string, timestamp time.Time, bodyID string) error {
	if id == "" {
		return fmt.Errorf("execution: message id is required")
	}
	if timestamp.IsZero() {
		return fmt.Errorf("execution: timestamp is required")
	}
	_, offset := timestamp.Zone()
	if offset != 0 {
		return fmt.Errorf("execution: timestamp must be UTC")
	}
	if bodyID == "" {
		return fmt.Errorf("execution: body_id is required")
	}
	return nil
}

func (s Status) valid() bool {
	switch s {
	case StatusSuccess, StatusDenied, StatusUnsupported, StatusInvalid,
		StatusUnavailable, StatusFailed, StatusCancelled, StatusExpired:
		return true
	default:
		return false
	}
}
