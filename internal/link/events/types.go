package events

import "time"

// Type identifies the event kind.
type Type string

const (
	TypeMessage  Type = "message"
	TypeCommand  Type = "command"
	TypeSystem   Type = "system"
	TypePresence Type = "presence"
	TypeUnknown  Type = "unknown"
)

// Event is the generic inbound event envelope.
type Event struct {
	ID        string    `json:"id"`
	Type      Type      `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Payload   any       `json:"payload"`
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
	default:
		return TypeUnknown
	}
}