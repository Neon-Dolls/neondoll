package events

import "time"

// Type identifies the event kind.
type Type string

const (
	TypeMessage      Type = "message"
	TypeCommand      Type = "command"
	TypeSystem       Type = "system"
	TypePresence     Type = "presence"
	TypeInternalWake Type = "internal_wake"
	TypeUnknown      Type = "unknown"
)

// Event is the generic inbound event envelope.
type Event struct {
	ID            string    `json:"id"`
	Type          Type      `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	DollID        string    `json:"doll_id,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Payload       any       `json:"payload"`
}

// MessagePayload for standard chat messages.
type MessagePayload struct {
	Text      string `json:"text"`
	ChannelID string `json:"channel_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	UserName  string `json:"user_name,omitempty"`
}

// CommandPayload for slash commands and directives.
type CommandPayload struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	Flags      map[string]string `json:"flags,omitempty"`
	Raw        string            `json:"raw,omitempty"`
}

// SystemPayload for lifecycle and system events.
type SystemPayload struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

// PresencePayload for online/offline status.
type PresencePayload struct {
	UserID string `json:"user_id"`
	Status string `json:"status"`
}

// IntentionWakePayload for internal wake events — a pending Intention
// has become due and Spark should reconsider it.
type IntentionWakePayload struct {
	IntentionID string `json:"intention_id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
}

// New creates an Event with the current timestamp.
func New(id string, typ Type, source string, payload any) Event {
	return Event{
		ID:        id,
		Type:      typ,
		Timestamp: time.Now().UTC(),
		Source:    source,
		Payload:   payload,
	}
}

// NewDollMessage creates a message Event addressed to a specific Doll.
func NewDollMessage(id, dollID, text string) Event {
	return Event{
		ID:     id,
		Type:   TypeMessage,
		Source: "client",
		DollID: dollID,
		Timestamp: time.Now().UTC(),
		Payload: MessagePayload{
			Text: text,
		},
	}
}

// NewResponse creates a response Event that echoes the correlation ID.
func NewResponse(correlationID, dollID, text string) Event {
	return Event{
		ID:            "",
		Type:          TypeMessage,
		Source:        "core",
		DollID:        dollID,
		CorrelationID: correlationID,
		Timestamp:     time.Now().UTC(),
		Payload: MessagePayload{
			Text: text,
		},
	}
}

// NewErrorResponse creates a system Event reporting an error.
func NewErrorResponse(correlationID, dollID, message string) Event {
	return Event{
		ID:            "",
		Type:          TypeSystem,
		Source:        "core",
		DollID:        dollID,
		CorrelationID: correlationID,
		Timestamp:     time.Now().UTC(),
		Payload: SystemPayload{
			Event: "error",
			Data:  message,
		},
	}
}

// ParseType converts a string to a Type.
func ParseType(s string) Type {
	switch s {
	case "message":
		return TypeMessage
	case "command":
		return TypeCommand
	case "system":
		return TypeSystem
	case "presence":
		return TypePresence
	case "internal_wake":
		return TypeInternalWake
	default:
		return TypeUnknown
	}
}