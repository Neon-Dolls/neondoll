// Package permissionrequest owns the durable lifecycle of Core permission
// requests. It records decisions but deliberately does not execute actions.
package permissionrequest

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
)

type ID string
type Origin string
type Status string

const (
	OriginPolicyRequired Origin = "policy_required"
	OriginVoluntary      Origin = "voluntary"

	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

var (
	ErrNotFound          = errors.New("permission request: not found")
	ErrInvalidRequest    = errors.New("permission request: invalid request")
	ErrInvalidTransition = errors.New("permission request: invalid lifecycle transition")
)

// Correlation keeps permission decisions traceable to the execution that
// requested them without conflating Session, Turn, Cognitive Run, or tool IDs.
type Correlation struct {
	CognitiveRunID string `json:"cognitive_run_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
}

type Request struct {
	ID                 ID              `json:"request_id"`
	DollID             string          `json:"doll_id"`
	OwnerID            string          `json:"owner_id"`
	Origin             Origin          `json:"origin"`
	Action             string          `json:"action,omitempty"`
	Question           string          `json:"question,omitempty"`
	Capability         string          `json:"capability,omitempty"`
	Operation          string          `json:"operation,omitempty"`
	Arguments          json.RawMessage `json:"arguments,omitempty"`
	Status             Status          `json:"status"`
	CreatedAt          time.Time       `json:"created_at"`
	ExpiresAt          time.Time       `json:"expires_at"`
	ResolvedAt         *time.Time      `json:"resolved_at,omitempty"`
	ResolvingAuthority string          `json:"resolving_authority,omitempty"`
	Correlation        Correlation     `json:"correlation,omitempty"`
}

type CreateRequest struct {
	DollID      string
	OwnerID     string
	Origin      Origin
	Action      string
	Question    string
	Capability  string
	Operation   string
	Arguments   json.RawMessage
	ExpiresAt   time.Time
	Correlation Correlation
	// PolicyDecision is required for policy-required requests and prevents a
	// prohibited action from being converted into an approval request.
	PolicyDecision actionpolicy.Decision
}

type Service interface {
	Create(context.Context, CreateRequest) (*Request, error)
	Get(context.Context, ID) (*Request, error)
	ListPending(context.Context) ([]*Request, error)
	Approve(context.Context, ID, string) (*Request, error)
	Deny(context.Context, ID, string) (*Request, error)
	Cancel(context.Context, ID) (*Request, error)
	Expire(context.Context, ID) (*Request, error)
}

type Store interface {
	CreatePermissionRequest(context.Context, *Request) error
	LoadPermissionRequest(context.Context, ID) (*Request, error)
	ListPendingPermissionRequests(context.Context) ([]*Request, error)
	TransitionPermissionRequest(context.Context, ID, Status, time.Time, string) (bool, error)
}
