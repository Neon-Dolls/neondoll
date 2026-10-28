// Package permissionrequest owns the durable lifecycle of Core permission
// requests. It records decisions but deliberately does not execute actions.
package permissionrequest

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type ID string
type Origin string
type Status string
type ContinuationStatus string
type NotificationStatus string

const (
	OriginPolicyRequired Origin = "policy_required"
	OriginVoluntary      Origin = "voluntary"

	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"

	ContinuationNone      ContinuationStatus = ""
	ContinuationWaiting   ContinuationStatus = "waiting"
	ContinuationReady     ContinuationStatus = "ready"
	ContinuationRunning   ContinuationStatus = "running"
	ContinuationSucceeded ContinuationStatus = "succeeded"
	ContinuationFailed    ContinuationStatus = "failed"
	ContinuationDenied    ContinuationStatus = "denied"

	NotificationNone       NotificationStatus = ""
	NotificationPending    NotificationStatus = "pending"
	NotificationDelivering NotificationStatus = "delivering"
	NotificationDelivered  NotificationStatus = "delivered"
)

var (
	ErrNotFound            = errors.New("permission request: not found")
	ErrInvalidRequest      = errors.New("permission request: invalid request")
	ErrInvalidTransition   = errors.New("permission request: invalid lifecycle transition")
	ErrUnauthenticated     = errors.New("permission request: unauthenticated owner")
	ErrUnauthorized        = errors.New("permission request: unauthorized owner")
	ErrExpired             = errors.New("permission request: expired")
	ErrContinuationClaimed = errors.New("permission request: continuation already claimed")
	ErrNotificationClaimed = errors.New("permission request: outcome notification already claimed")
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
	ID                      ID                 `json:"request_id"`
	DollID                  string             `json:"doll_id"`
	OwnerID                 string             `json:"owner_id"`
	Origin                  Origin             `json:"origin"`
	Action                  string             `json:"action,omitempty"`
	Question                string             `json:"question,omitempty"`
	Capability              string             `json:"capability,omitempty"`
	Operation               string             `json:"operation,omitempty"`
	Arguments               json.RawMessage    `json:"arguments,omitempty"`
	Status                  Status             `json:"status"`
	CreatedAt               time.Time          `json:"created_at"`
	ExpiresAt               time.Time          `json:"expires_at"`
	ResolvedAt              *time.Time         `json:"resolved_at,omitempty"`
	ResolvingAuthority      string             `json:"resolving_authority,omitempty"`
	Correlation             Correlation        `json:"correlation,omitempty"`
	ContinuationStatus      ContinuationStatus `json:"continuation_status,omitempty"`
	ContinuationResult      json.RawMessage    `json:"continuation_result,omitempty"`
	ContinuationUpdatedAt   *time.Time         `json:"continuation_updated_at,omitempty"`
	NotificationStatus      NotificationStatus `json:"notification_status,omitempty"`
	NotificationLeaseUntil  *time.Time         `json:"notification_lease_until,omitempty"`
	NotificationDeliveredAt *time.Time         `json:"notification_delivered_at,omitempty"`
	NotificationAttempts    int                `json:"notification_attempts,omitempty"`
	NotificationError       string             `json:"notification_error,omitempty"`
}

type ContinuationOutcome struct {
	Success     bool            `json:"success"`
	ExecutionID string          `json:"execution_id,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Error       string          `json:"error,omitempty"`
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
	ListReadyPermissionContinuations(context.Context) ([]*Request, error)
	ClaimPermissionContinuation(context.Context, ID, time.Time) (bool, error)
	CompletePermissionContinuation(context.Context, ID, ContinuationStatus, json.RawMessage, time.Time) (bool, error)
	ListPermissionOutcomeNotifications(context.Context, time.Time) ([]*Request, error)
	ClaimPermissionOutcomeNotification(context.Context, ID, time.Time, time.Time) (bool, error)
	CompletePermissionOutcomeNotification(context.Context, ID, time.Time) (bool, error)
	ReleasePermissionOutcomeNotification(context.Context, ID, string, time.Time) (bool, error)
}

// ResolutionHandler continues a durable decision after the authenticated
// boundary records it. Implementations must be idempotent across replay.
type ResolutionHandler interface {
	HandlePermissionResolution(context.Context, *Request) error
}

type ResolutionReplayer interface {
	ReplayPermissionResolutions(context.Context) error
}

func OutcomeNotificationID(id ID) string { return "permission-outcome:" + string(id) }
