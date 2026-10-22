package sessioncontrol_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/SessionControl"
)

func TestRequestWireShapeAndValidation(t *testing.T) {
	request := sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "request-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-1"}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"type":"session.select","id":"request-1","payload":{"session_id":"session-1"}}`
	if string(encoded) != want {
		t.Fatalf("wire request = %s, want %s", encoded, want)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestInteractionWireShapeAndValidation(t *testing.T) {
	event := events.NewDollMessage("event-1", "doll-1", "hello")
	interaction := sessioncontrol.Interaction{Version: 1, Type: sessioncontrol.TypeInteraction, SessionID: "session-1", Event: event}
	encoded, err := json.Marshal(interaction)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 4 || string(decoded["version"]) != "1" || string(decoded["type"]) != `"interaction"` || string(decoded["session_id"]) != `"session-1"` {
		t.Fatalf("wire interaction = %s", encoded)
	}
	if err := interaction.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	interaction.Version = 2
	if err := interaction.Validate(); !errors.Is(err, sessioncontrol.ErrUnsupportedVersion) {
		t.Fatalf("version error = %v", err)
	}
	interaction.Version = 1
	interaction.SessionID = ""
	if err := interaction.Validate(); !errors.Is(err, sessioncontrol.ErrInvalidRequest) {
		t.Fatalf("selector error = %v", err)
	}
}

func TestRequestRejectsUnsupportedVersionAndInvalidPayloads(t *testing.T) {
	tests := []struct {
		name    string
		request sessioncontrol.Request
		want    error
	}{
		{"unsupported version", sessioncontrol.Request{Version: 2, Type: sessioncontrol.TypeList, ID: "request-1"}, sessioncontrol.ErrUnsupportedVersion},
		{"missing correlation ID", sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeList}, sessioncontrol.ErrInvalidRequest},
		{"select without session", sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "request-1"}, sessioncontrol.ErrInvalidRequest},
		{"list with payload", sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeList, ID: "request-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-1"}}, sessioncontrol.ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate error = %v, want %v", err, test.want)
			}
		})
	}
}
