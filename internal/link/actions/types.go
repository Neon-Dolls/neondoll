package actions

// Type identifies the kind of outbound action.
type Type string

const (
	TypeSendMessage    Type = "send_message"
	TypeExecuteCommand Type = "execute_command"
	TypeUpdateStatus   Type = "update_status"
	TypeWebhook        Type = "webhook"
	TypeCustom         Type = "custom"
)

// Action is an outbound action the Doll takes.
type Action struct {
	ID        string       `json:"id"`
	Type      Type         `json:"type"`
	Target    string       `json:"target"`
	Payload   any          `json:"payload"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// SendMessagePayload for sending a message.
type SendMessagePayload struct {
	Text      string `json:"text"`
	ChannelID string `json:"channel_id,omitempty"`
	ReplyTo   string `json:"reply_to,omitempty"`
}

// ExecuteCommandPayload for running a command.
type ExecuteCommandPayload struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// UpdateStatusPayload for changing Doll's status.
type UpdateStatusPayload struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// WebhookPayload for dispatching a webhook.
type WebhookPayload struct {
	URL     string `json:"url"`
	Method  string `json:"method"`
	Body    any    `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// New creates an Action.
func New(id string, typ Type, target string, payload any) Action {
	return Action{
		ID:      id,
		Type:    typ,
		Target:  target,
		Payload: payload,
	}
}

// ParseType converts a string to an Action Type.
func ParseType(s string) Type {
	switch s {
	case "send_message":
		return TypeSendMessage
	case "execute_command":
		return TypeExecuteCommand
	case "update_status":
		return TypeUpdateStatus
	case "webhook":
		return TypeWebhook
	default:
		return TypeCustom
	}
}