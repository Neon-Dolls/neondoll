package communication_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	externalidentity "github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
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
	directory, _ := externalidentity.NewMemoryResolver([]externalidentity.Mapping{{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-1", Platform: "test", SubjectID: "42"}, Principal: externalidentity.Principal{ID: "owner-1", Authority: externalidentity.AuthorityOwner}}})
	service, err := communication.New(directory, sessions, sessions, events)
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
	directory, _ := externalidentity.NewMemoryResolver(nil)
	service, _ := communication.New(directory, sessions, sessions, events)
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
	directory, _ := externalidentity.NewMemoryResolver([]externalidentity.Mapping{{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "42"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}}})
	service, _ := communication.New(directory, sessions, sessions, events)
	if _, err = service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner", Text: "before revocation"}); err != nil {
		t.Fatal(err)
	}
	if err = sessions.ReconcileParticipantSources(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	revokedDirectory, _ := externalidentity.NewMemoryResolver(nil)
	service, _ = communication.New(revokedDirectory, sessions, sessions, events)
	if _, err = service.SendText(context.Background(), communication.SendTextRequest{RecipientPrincipalID: "owner", Text: "after revocation"}); !errors.Is(err, communication.ErrUnknownRecipient) {
		t.Fatalf("err=%v", err)
	}
	peers, err := service.ListPeers(context.Background())
	if err != nil || len(peers) != 0 {
		t.Fatalf("revoked peers=%#v err=%v", peers, err)
	}
}

func TestListPeersReflectsConversationReassignmentWithoutRemovingOtherParticipants(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	conversation, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "group"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []session.ParticipantSource{
		{SessionID: conversation.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "changing", PrincipalID: "old-peer"},
		{SessionID: conversation.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member", PrincipalID: "member"},
	} {
		if _, err = sessions.BindResolvedParticipant(context.Background(), conversation.ID, source); err != nil {
			t.Fatal(err)
		}
	}
	if err = sessions.ReconcileParticipantSources(context.Background(), []session.ParticipantSource{
		{SessionID: conversation.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "changing", PrincipalID: "new-peer"},
		{SessionID: conversation.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member", PrincipalID: "member"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindResolvedParticipant(context.Background(), conversation.ID, session.ParticipantSource{
		SessionID: conversation.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "changing", PrincipalID: "new-peer",
	}); err != nil {
		t.Fatal(err)
	}
	directory, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "changing"}, Principal: externalidentity.Principal{ID: "new-peer", Authority: externalidentity.AuthorityPeer}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member"}, Principal: externalidentity.Principal{ID: "member", Authority: externalidentity.AuthorityParticipant}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := sessionevent.New(store.(sessionevent.Store), sessions, sessions, &transport{})
	service, _ := communication.New(directory, sessions, sessions, events)
	peers, err := service.ListPeers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]communication.Peer, len(peers))
	for _, peer := range peers {
		byID[peer.PrincipalID] = peer
	}
	if _, exists := byID["old-peer"]; exists || !byID["new-peer"].ActiveGatewayRoute || !byID["member"].ActiveGatewayRoute {
		t.Fatalf("peers=%#v", peers)
	}
}

func TestListPeersReportsTrustedRelationshipsAndSharedRouteEligibility(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	active, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "active"})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindResolvedParticipant(context.Background(), active.ID, session.ParticipantSource{SessionID: active.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "owner-a", PrincipalID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindResolvedParticipant(context.Background(), closed.ID, session.ParticipantSource{SessionID: closed.ID, GatewayInstanceID: "gateway", Platform: "matrix", SubjectID: "owner-b", PrincipalID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.BindResolvedParticipant(context.Background(), active.ID, session.ParticipantSource{SessionID: active.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member", PrincipalID: "member"}); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Close(context.Background(), closed.ID); err != nil {
		t.Fatal(err)
	}
	directory, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "owner-a"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "matrix", SubjectID: "owner-b"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member"}, Principal: externalidentity.Principal{ID: "member", Authority: externalidentity.AuthorityParticipant}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "offline"}, Principal: externalidentity.Principal{ID: "offline", Authority: externalidentity.AuthorityPeer}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := sessionevent.New(store.(sessionevent.Store), sessions, sessions, &transport{})
	service, _ := communication.New(directory, sessions, sessions, events)
	peers, err := service.ListPeers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 3 {
		t.Fatalf("peers=%#v", peers)
	}
	byID := map[string]communication.Peer{}
	for _, peer := range peers {
		byID[peer.PrincipalID] = peer
	}
	owner := byID["owner"]
	if !owner.ActiveGatewayRoute || !owner.RelationshipAvailable || owner.EligibleSessionCount != 1 || owner.KnownSessionCount != 2 || owner.IdentityCount != 2 {
		t.Fatalf("owner=%#v", owner)
	}
	if len(owner.Platforms) != 2 || owner.Platforms[0] != "matrix" || owner.Platforms[1] != "telegram" {
		t.Fatalf("platforms=%#v", owner.Platforms)
	}
	member := byID["member"]
	if !member.ActiveGatewayRoute || member.Authority != externalidentity.AuthorityParticipant {
		t.Fatalf("member=%#v", member)
	}
	offline := byID["offline"]
	if offline.ActiveGatewayRoute || offline.RelationshipAvailable || offline.KnownSessionCount != 0 {
		t.Fatalf("offline=%#v", offline)
	}
	for _, peer := range peers {
		if strings.Contains(fmt.Sprintf("%#v", peer), "owner-a") || strings.Contains(fmt.Sprintf("%#v", peer), "gateway") {
			t.Fatalf("transport identity leaked: %#v", peer)
		}
	}
}
