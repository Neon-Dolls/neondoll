// Package sessioncontrol defines the versioned Doll Link messages used to
// manage and select Core Interaction Sessions. It contains wire types only;
// Core session lifecycle and authorization remain outside this package.
package sessioncontrol

import (
	"time"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

const Version = 1

type MessageType string
type Status string
type ErrorCode string

const (
	TypeCreate MessageType = "session.create"
	TypeList   MessageType = "session.list"
	TypeSelect MessageType = "session.select"
	TypeClose  MessageType = "session.close"
	TypeResult MessageType = "session.result"
	// TypeInteraction wraps an otherwise unchanged legacy event with an
	// explicit per-interaction session selector.
	TypeInteraction MessageType = "interaction"

	StatusOK    Status = "ok"
	StatusError Status = "error"

	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorUnsupportedVersion ErrorCode = "unsupported_version"
	ErrorRequestIDConflict  ErrorCode = "request_id_conflict"
	ErrorUnauthenticated    ErrorCode = "unauthenticated"
	ErrorHandlerUnavailable ErrorCode = "handler_unavailable"
	ErrorSessionUnavailable ErrorCode = "session_unavailable"
	ErrorInternal           ErrorCode = "internal_error"
)

// Interaction routes one legacy event through an explicit Core Interaction
// Session without modifying the connection-wide selection.
type Interaction struct {
	Version   int          `json:"version"`
	Type      MessageType  `json:"type"`
	SessionID string       `json:"session_id"`
	Event     events.Event `json:"event"`
}

// Request is the common request envelope. Payload fields not used by an
// operation must be omitted. ID is supplied by the Body and is echoed by the
// Core as the result correlation_id.
type Request struct {
	Version int            `json:"version"`
	Type    MessageType    `json:"type"`
	ID      string         `json:"id"`
	Payload RequestPayload `json:"payload"`
}

type RequestPayload struct {
	ExternalConversationRef string `json:"external_conversation_ref,omitempty"`
	SessionID               string `json:"session_id,omitempty"`
}

// Response is the single Core-to-Body response envelope for all session
// control operations. Foreign, unknown, and closed sessions intentionally use
// the same error code so authorization failures disclose no ownership data.
type Response struct {
	Version       int             `json:"version"`
	Type          MessageType     `json:"type"`
	ID            string          `json:"id"`
	CorrelationID string          `json:"correlation_id"`
	Payload       ResponsePayload `json:"payload"`
}

type ResponsePayload struct {
	Operation MessageType `json:"operation"`
	Status    Status      `json:"status"`
	Session   *Session    `json:"session,omitempty"`
	Sessions  []Session   `json:"sessions,omitempty"`
	Error     *Error      `json:"error,omitempty"`
}

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Session is the Body-visible subset of a Core Interaction Session. Owner is
// deliberately omitted because every response is scoped to the authenticated
// connection identity.
type Session struct {
	ID                      string    `json:"session_id"`
	ExternalConversationRef string    `json:"external_conversation_ref,omitempty"`
	Status                  string    `json:"status"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}
