package sessionevent_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

func TestVersionedEventSupportsStructuredPartsAndPermissionPayloads(t *testing.T) {
	now := time.Now().UTC()
	message, err := sessionevent.NewEvent("session-1", sessionevent.TypeMessage, sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{
		{Type: sessionevent.PartText, Text: "caption"},
		{Type: sessionevent.PartFile, MediaID: "media-1", Filename: "report.pdf", MIMEType: "application/pdf", SizeBytes: 42},
	}}, sessionevent.Correlation{TurnID: "turn-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if message.Version != sessionevent.Version || message.ID == "" || message.SessionID != session.ID("session-1") {
		t.Fatalf("event envelope = %#v", message)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded sessionevent.Event
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Validate() != nil {
		t.Fatalf("round trip = %#v, unmarshal=%v validate=%v", decoded, err, decoded.Validate())
	}

	decision, err := sessionevent.NewEvent("session-1", sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: "request-1", Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: "request-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := sessionevent.DecodePermissionDecision(decision)
	if err != nil || payload.RequestID != "request-1" || payload.Decision != permissionrequest.DecisionApprove {
		t.Fatalf("decision payload = %#v, %v", payload, err)
	}
}

func TestEventRejectsInlineMediaAndUnsupportedVersions(t *testing.T) {
	_, err := sessionevent.NewEvent("session-1", sessionevent.TypeMessage,
		sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartFile, Filename: "inline-without-reference"}}},
		sessionevent.Correlation{}, time.Now())
	if err == nil {
		t.Fatal("media without a stable reference was accepted")
	}
	event, err := sessionevent.NewEvent("session-1", sessionevent.TypeStatus,
		sessionevent.StatusPayload{Status: "ready"}, sessionevent.Correlation{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	event.Version++
	if event.Validate() == nil {
		t.Fatal("unsupported protocol version was accepted")
	}
}
