package actions

import "testing"

func TestNewAction(t *testing.T) {
	a := New("act-1", TypeSendMessage, "discord:ch-1",
		SendMessagePayload{Text: "Hello!", ChannelID: "ch-1"})
	if a.ID != "act-1" {
		t.Errorf("expected act-1, got %s", a.ID)
	}
	if a.Type != TypeSendMessage {
		t.Errorf("expected TypeSendMessage, got %v", a.Type)
	}
	if a.Target != "discord:ch-1" {
		t.Errorf("expected discord:ch-1, got %s", a.Target)
	}
	payload, ok := a.Payload.(SendMessagePayload)
	if !ok {
		t.Fatal("expected SendMessagePayload")
	}
	if payload.Text != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", payload.Text)
	}
}

func TestParseType(t *testing.T) {
	tests := []struct {
		input string
		want  Type
	}{
		{"send_message", TypeSendMessage},
		{"execute_command", TypeExecuteCommand},
		{"update_status", TypeUpdateStatus},
		{"webhook", TypeWebhook},
		{"custom", TypeCustom},
		{"unknown", TypeCustom},
	}
	for _, tt := range tests {
		if got := ParseType(tt.input); got != tt.want {
			t.Errorf("ParseType(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestExecuteCommandPayload(t *testing.T) {
	p := ExecuteCommandPayload{Command: "roll", Args: []string{"d20"}}
	if p.Command != "roll" {
		t.Errorf("expected roll, got %s", p.Command)
	}
	if len(p.Args) != 1 || p.Args[0] != "d20" {
		t.Errorf("expected [d20], got %v", p.Args)
	}
}

func TestUpdateStatusPayload(t *testing.T) {
	p := UpdateStatusPayload{Status: "busy", Detail: "Thinking"}
	if p.Status != "busy" {
		t.Errorf("expected busy, got %s", p.Status)
	}
}

func TestWebhookPayload(t *testing.T) {
	p := WebhookPayload{
		URL:    "https://example.com/hook",
		Method: "POST",
		Body:   map[string]string{"event": "test"},
		Headers: map[string]string{"Authorization": "Bearer token"},
	}
	if p.URL != "https://example.com/hook" {
		t.Errorf("expected hook URL, got %s", p.URL)
	}
}