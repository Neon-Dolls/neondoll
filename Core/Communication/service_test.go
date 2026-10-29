package communication_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	sessionevent "github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

type transport struct {
	deliveries []sessionevent.Delivery
	err        error
}

func (t *transport) DeliverSessionEvent(_ context.Context, _ session.Owner, delivery sessionevent.Delivery) error {
	t.deliveries = append(t.deliveries, delivery)
	return t.err
}

func TestSendTextUsesKnownPrincipalSessionAndDurableDelivery(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	created, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-1"}, ExternalConversationRef: "conversation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindParticipant(context.Background(), created.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
	deliveryTransport := &transport{}
	events, err := sessionevent.New(store.(sessionevent.Store), sessions, sessions, deliveryTransport)
	if err != nil {
		t.Fatal(err)
	}
	service, err := communication.New(sessions, sessions, events)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner-1", Text: "autonomous hello", Correlation: communication.Correlation{CognitiveRunID: "run-1", IntentionID: "intention-1", ToolCallID: "call-1"}})
	if err != nil || result.Status != sessionevent.DeliveryAccepted || result.DeliveryID == "" || result.SessionID != created.ID {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(deliveryTransport.deliveries) != 1 {
		t.Fatalf("deliveries=%#v", deliveryTransport.deliveries)
	}
	delivery := deliveryTransport.deliveries[0]
	if delivery.TargetPrincipalID != "owner-1" || delivery.Event.Correlation.IntentionID != "intention-1" || delivery.Event.Correlation.CognitiveRunID != "run-1" {
		t.Fatalf("delivery=%#v", delivery)
	}

	deliveryTransport.err = errors.New("gateway temporarily unavailable")
	pending, err := service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner-1", Text: "retry me"})
	if err == nil || pending.Status != sessionevent.DeliveryPending || pending.DeliveryID == "" {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	stored, err := events.GetDelivery(context.Background(), pending.DeliveryID)
	if err != nil || stored.Status != sessionevent.DeliveryPending {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	deliveryTransport.err = nil
	replayed, err := events.RetryDelivery(context.Background(), pending.DeliveryID)
	if err != nil || replayed.Status != sessionevent.DeliveryAccepted {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if len(deliveryTransport.deliveries) != 3 || deliveryTransport.deliveries[1].ID != pending.DeliveryID || deliveryTransport.deliveries[2].ID != pending.DeliveryID {
		t.Fatalf("replay deliveries=%#v", deliveryTransport.deliveries)
	}
}

func TestSendTextRejectsUnknownOrUnroutableRecipient(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	events, _ := sessionevent.New(store.(sessionevent.Store), sessions, sessions, &transport{})
	service, _ := communication.New(sessions, sessions, events)
	if _, err = service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "forged", Text: "hello"}); !errors.Is(err, communication.ErrUnknownRecipient) {
		t.Fatalf("err=%v", err)
	}
}

func TestRevokedIdentityCannotRemainOutboundTarget(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	created, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindResolvedParticipant(context.Background(), created.ID, session.ParticipantSource{SessionID: created.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "42", PrincipalID: "owner"}); err != nil {
		t.Fatal(err)
	}
	events, _ := sessionevent.New(store.(sessionevent.Store), sessions, sessions, &transport{})
	service, _ := communication.New(sessions, sessions, events)
	if _, err = service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner", Text: "before revocation"}); err != nil {
		t.Fatal(err)
	}
	if err = sessions.ReconcileParticipantSources(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner", Text: "after revocation"}); !errors.Is(err, communication.ErrUnknownRecipient) {
		t.Fatalf("err=%v", err)
	}
}
