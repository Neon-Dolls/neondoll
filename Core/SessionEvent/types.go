// Package sessionevent defines Core's versioned, transport-neutral Interaction
// Session event protocol and durable outbound delivery boundary.
package sessionevent

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/google/uuid"
)

const Version = 1

type Type string
type PartType string
type DeliveryStatus string

const (
	TypeMessage             Type = "message"
	TypePermissionRequested Type = "permission.requested"
	TypePermissionDecided   Type = "permission.decided"
	TypeStatus              Type = "status"
	TypeError               Type = "error"

	PartText  PartType = "text"
	PartImage PartType = "image"
	PartFile  PartType = "file"
	PartAudio PartType = "audio"
	PartVideo PartType = "video"

	DeliveryPending  DeliveryStatus = "pending"
	DeliveryAccepted DeliveryStatus = "accepted"
	DeliveryFailed   DeliveryStatus = "failed"
)

var (
	ErrInvalidEvent         = errors.New("session event: invalid event")
	ErrSessionUnavailable   = errors.New("session event: session unavailable")
	ErrRecipientUnavailable = errors.New("session event: recipient unavailable")
	ErrTransportUnavailable = errors.New("session event: transport unavailable")
	ErrPresentationNotFound = errors.New("session event: permission presentation not found")
)

type Correlation struct {
	CausationEventID string `json:"causation_event_id,omitempty"`
	TurnID           string `json:"turn_id,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	CognitiveRunID   string `json:"cognitive_run_id,omitempty"`
	IntentionID      string `json:"intention_id,omitempty"`
}

type Event struct {
	Version     int             `json:"version"`
	ID          string          `json:"event_id"`
	SessionID   session.ID      `json:"session_id"`
	Type        Type            `json:"type"`
	Timestamp   time.Time       `json:"timestamp"`
	Correlation Correlation     `json:"correlation,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

type MessagePart struct {
	Type      PartType `json:"type"`
	Text      string   `json:"text,omitempty"`
	MediaID   string   `json:"media_id,omitempty"`
	Filename  string   `json:"filename,omitempty"`
	MIMEType  string   `json:"mime_type,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
}

type MessagePayload struct {
	Parts []MessagePart `json:"parts"`
}

type ActionDetails struct {
	Action     string          `json:"action,omitempty"`
	Capability string          `json:"capability,omitempty"`
	Operation  string          `json:"operation,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
}

type PermissionRequestedPayload struct {
	RequestID string        `json:"request_id"`
	Question  string        `json:"question"`
	Origin    string        `json:"origin"`
	Action    ActionDetails `json:"action,omitempty"`
	ExpiresAt time.Time     `json:"expires_at"`
}

type PermissionDecidedPayload struct {
	RequestID string                     `json:"request_id"`
	Decision  permissionrequest.Decision `json:"decision"`
}

type StatusPayload struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Delivery struct {
	ID                string         `json:"delivery_id"`
	Event             Event          `json:"event"`
	TargetPrincipalID string         `json:"target_principal_id,omitempty"`
	Owner             session.Owner  `json:"owner"`
	Status            DeliveryStatus `json:"status"`
	Error             string         `json:"error,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// PermissionPresentation durably binds a permission request to the exact
// Session and delivery through which it was presented. Session membership or
// Owner authority alone cannot authorize a decision through another Session.
type PermissionPresentation struct {
	RequestID  string     `json:"request_id"`
	SessionID  session.ID `json:"session_id"`
	DeliveryID string     `json:"delivery_id"`
	CreatedAt  time.Time  `json:"created_at"`
}

func NewEvent(sessionID session.ID, typ Type, payload any, correlation Correlation, now time.Time) (Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, ErrInvalidEvent
	}
	event := Event{Version: Version, ID: uuid.NewString(), SessionID: sessionID, Type: typ, Timestamp: now.UTC(), Correlation: correlation, Payload: encoded}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (event Event) Validate() error {
	if event.Version != Version || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(string(event.SessionID)) == "" || event.Timestamp.IsZero() || !json.Valid(event.Payload) {
		return ErrInvalidEvent
	}
	switch event.Type {
	case TypeMessage:
		var payload MessagePayload
		if json.Unmarshal(event.Payload, &payload) != nil || len(payload.Parts) == 0 {
			return ErrInvalidEvent
		}
		for _, part := range payload.Parts {
			if !validPart(part) {
				return ErrInvalidEvent
			}
		}
	case TypePermissionRequested:
		var payload PermissionRequestedPayload
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.RequestID) == "" || strings.TrimSpace(payload.Question) == "" || payload.ExpiresAt.IsZero() {
			return ErrInvalidEvent
		}
		if event.Correlation.RequestID != "" && event.Correlation.RequestID != payload.RequestID {
			return ErrInvalidEvent
		}
	case TypePermissionDecided:
		var payload PermissionDecidedPayload
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.RequestID) == "" || (payload.Decision != permissionrequest.DecisionApprove && payload.Decision != permissionrequest.DecisionDeny) {
			return ErrInvalidEvent
		}
		if event.Correlation.RequestID != "" && event.Correlation.RequestID != payload.RequestID {
			return ErrInvalidEvent
		}
	case TypeStatus:
		var payload StatusPayload
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.Status) == "" {
			return ErrInvalidEvent
		}
	case TypeError:
		var payload ErrorPayload
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.Code) == "" || strings.TrimSpace(payload.Message) == "" {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	return nil
}

func validPart(part MessagePart) bool {
	switch part.Type {
	case PartText:
		return strings.TrimSpace(part.Text) != "" && part.MediaID == ""
	case PartImage, PartFile, PartAudio, PartVideo:
		return strings.TrimSpace(part.MediaID) != "" && part.SizeBytes >= 0
	default:
		return false
	}
}
