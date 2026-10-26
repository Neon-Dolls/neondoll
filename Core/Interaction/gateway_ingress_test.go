package interaction_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type principalHandler struct {
	mu        sync.Mutex
	principal externalidentity.Principal
	mapped    bool
	text      string
}

func (h *principalHandler) HandleInteraction(ctx context.Context, _ *session.InteractionSession, event *events.Event) (*events.Event, error) {
	principal, mapped := interaction.ResolvedPrincipal(ctx)
	payload, _ := event.Payload.(events.MessagePayload)
	h.mu.Lock()
	h.principal, h.mapped, h.text = principal, mapped, payload.Text
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, "routed")
	return &response, nil
}

func gatewayResolver(t *testing.T) *externalidentity.MemoryResolver {
	t.Helper()
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{
		{
			Identity:  externalidentity.Identity{GatewayInstanceID: "gateway-one", Platform: "telegram", SubjectID: "owner-42"},
			Principal: externalidentity.Principal{ID: "owner-zero", Authority: externalidentity.AuthorityOwner},
		},
		{
			Identity:  externalidentity.Identity{GatewayInstanceID: "gateway-one", Platform: "telegram", SubjectID: "member-7"},
			Principal: externalidentity.Principal{ID: "member-seven", Authority: externalidentity.AuthorityParticipant},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func createGatewayIngress(t *testing.T, gateway string) (*interaction.GatewayIngress, session.ID, *principalHandler, session.Service) {
	t.Helper()
	sessions := openRouterSessions(t)
	created, err := sessions.Create(context.Background(), session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: gateway}, ExternalConversationRef: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &principalHandler{}
	router, err := interaction.NewRouter(sessions, handler)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := interaction.NewGatewayIngress(externalidentity.GatewayInstanceID(gateway), gatewayResolver(t), router)
	if err != nil {
		t.Fatal(err)
	}
	return ingress, created.ID, handler, sessions
}

func sendGatewayText(t *testing.T, ingress *interaction.GatewayIngress, sessionID session.ID, subject string, claims map[string]string) error {
	t.Helper()
	event := events.NewDollMessage("event-1", "doll-1", "legacy text remains intact")
	response, err := ingress.HandleVerifiedInteraction(context.Background(), sessionID, interaction.VerifiedExternalSender{
		Platform: "telegram", SubjectID: subject, Claims: claims,
	}, &event)
	if err == nil && response.CorrelationID != event.ID {
		t.Fatalf("response correlation = %q", response.CorrelationID)
	}
	return err
}

func TestGatewayIngressResolvesConfiguredOwnerAndPreservesTextRouting(t *testing.T) {
	ingress, sessionID, handler, _ := createGatewayIngress(t, "gateway-one")
	if err := sendGatewayText(t, ingress, sessionID, "owner-42", nil); err != nil {
		t.Fatal(err)
	}
	if !handler.mapped || handler.principal.ID != "owner-zero" || handler.principal.Authority != externalidentity.AuthorityOwner {
		t.Fatalf("resolved principal = %#v, mapped=%v", handler.principal, handler.mapped)
	}
	if handler.text != "legacy text remains intact" {
		t.Fatalf("text = %q", handler.text)
	}
}

func TestGatewayIngressLeavesUnknownSenderUnmapped(t *testing.T) {
	ingress, sessionID, handler, _ := createGatewayIngress(t, "gateway-one")
	if err := sendGatewayText(t, ingress, sessionID, "unknown", nil); err != nil {
		t.Fatal(err)
	}
	if handler.mapped || handler.principal != (externalidentity.Principal{}) {
		t.Fatalf("unknown sender gained principal: %#v, mapped=%v", handler.principal, handler.mapped)
	}
}

func TestGatewayIngressWrongGatewayCannotBorrowMappingOrSession(t *testing.T) {
	ingress, sessionID, handler, sessions := createGatewayIngress(t, "gateway-two")
	if err := sendGatewayText(t, ingress, sessionID, "owner-42", nil); err != nil {
		t.Fatal(err)
	}
	if handler.mapped {
		t.Fatalf("wrong Gateway borrowed principal %#v", handler.principal)
	}
	foreign, err := sessions.Create(context.Background(), session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}, ExternalConversationRef: "foreign-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sendGatewayText(t, ingress, foreign.ID, "owner-42", nil); !errors.Is(err, interaction.ErrUnauthorizedSession) {
		t.Fatalf("foreign session error = %v", err)
	}
}

func TestGatewayIngressIgnoresForgedPrincipalClaims(t *testing.T) {
	ingress, sessionID, handler, _ := createGatewayIngress(t, "gateway-one")
	claims := map[string]string{"principal_id": "owner-zero", "authority": "owner", "role": "primary_peer"}
	if err := sendGatewayText(t, ingress, sessionID, "member-7", claims); err != nil {
		t.Fatal(err)
	}
	if !handler.mapped || handler.principal.ID != "member-seven" || handler.principal.Authority != externalidentity.AuthorityParticipant {
		t.Fatalf("forged claims changed principal: %#v", handler.principal)
	}
}
