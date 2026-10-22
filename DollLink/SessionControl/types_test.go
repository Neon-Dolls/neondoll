package sessioncontrol_test

import (
	"encoding/json"
	"errors"
	"testing"

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
