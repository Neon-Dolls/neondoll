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
	delivery := sessionevent.Delivery{
		ID: "delivery-1", Event: event, TargetPrincipalID: "owner-1",
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-1"}, Status: sessionevent.DeliveryPending,
	}
	want, err := gateway.NewOutboundEnvelope(delivery)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got gateway.OutboundEnvelope
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	var telegramShape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &telegramShape); err != nil {
		t.Fatal(err)
	}
	if len(telegramShape) != 3 || telegramShape["v"] == nil || telegramShape["type"] == nil || telegramShape["payload"] == nil {
		t.Fatalf("Telegram GatewayMessage shape = %s", encoded)
	}
	if telegramShape["abi_version"] != nil || telegramShape["delivery"] != nil {
		t.Fatalf("competing envelope fields present: %s", encoded)
	}
	decoded, err := got.DecodeDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != gateway.WASMEnvelopeVersion0 || got.Type != gateway.WASMTypeSessionEvent ||
		decoded.ID != delivery.ID || decoded.Event.ID != event.ID || decoded.TargetPrincipalID != "owner-1" {
		t.Fatalf("round trip = %#v", got)
	}
}
