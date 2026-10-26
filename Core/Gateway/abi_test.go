package gateway_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

func TestStructuredABIEnvelopeRoundTrip(t *testing.T) {
	event, err := sessionevent.NewEvent("session-1", sessionevent.TypeMessage,
		sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartText, Text: "hello"}}},
		sessionevent.Correlation{TurnID: "turn-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := gateway.OutboundEnvelope{ABIVersion: gateway.StructuredABIVersion, Delivery: sessionevent.Delivery{
		ID: "delivery-1", Event: event, TargetPrincipalID: "owner-1",
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-1"}, Status: sessionevent.DeliveryPending,
	}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got gateway.OutboundEnvelope
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.ABIVersion != gateway.StructuredABIVersion || got.Delivery.ID != want.Delivery.ID ||
		got.Delivery.Event.ID != event.ID || got.Delivery.TargetPrincipalID != "owner-1" {
		t.Fatalf("round trip = %#v", got)
	}
}
