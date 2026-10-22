package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

func openRuntimeServices(t *testing.T, path string) (*runtime.Services, persistence.Store) {
	t.Helper()
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sessionStore, ok := store.(session.Store)
	if !ok {
		store.Close()
		t.Fatal("persistence store does not implement session.Store")
	}
	services, err := runtime.NewServices(context.Background(), sessionStore)
	if err != nil {
		store.Close()
		t.Fatalf("NewServices: %v", err)
	}
	return services, store
}

func TestSessionServiceWiringAndRestartRecovery(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "neondoll.db")

	firstRuntime, firstStore := openRuntimeServices(t, dbPath)
	sessions := firstRuntime.Sessions()
	bodySession, err := sessions.Create(ctx, session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"},
	})
	if err != nil {
		t.Fatalf("create Body session through runtime: %v", err)
	}
	gatewaySession, err := sessions.Create(ctx, session.CreateRequest{
		Owner:                   session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"},
		ExternalConversationRef: "conversation-one",
	})
	if err != nil {
		t.Fatalf("create Gateway session through runtime: %v", err)
	}
	otherGatewaySession, err := sessions.Create(ctx, session.CreateRequest{
		Owner:                   session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-two"},
		ExternalConversationRef: "conversation-one",
	})
	if err != nil {
		t.Fatalf("create independent Gateway session through runtime: %v", err)
	}
	if bodySession.ID == gatewaySession.ID || bodySession.ID == otherGatewaySession.ID || gatewaySession.ID == otherGatewaySession.ID {
		t.Fatal("independent sessions share an ID")
	}
	closed, err := sessions.Close(ctx, bodySession.ID)
	if err != nil {
		t.Fatalf("close Body session: %v", err)
	}
	if closed.Status != session.StatusClosed {
		t.Fatalf("closed status = %q, want %q", closed.Status, session.StatusClosed)
	}
	// The session runtime does not own the persistent store or add shutdown
	// work; existing Core shutdown remains a clean store close.
	if err := firstStore.Close(); err != nil {
		t.Fatalf("Core shutdown store close: %v", err)
	}

	secondRuntime, secondStore := openRuntimeServices(t, dbPath)
	defer secondStore.Close()
	reconstructed := secondRuntime.Sessions()
	active, err := reconstructed.ListActive(ctx, nil)
	if err != nil {
		t.Fatalf("ListActive after Core restart: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active sessions after restart = %d, want 2", len(active))
	}
	activeIDs := map[session.ID]bool{}
	for _, current := range active {
		activeIDs[current.ID] = true
	}
	if !activeIDs[gatewaySession.ID] || !activeIDs[otherGatewaySession.ID] || activeIDs[bodySession.ID] {
		t.Fatalf("active sessions after restart = %+v", active)
	}
	persistedClosed, err := reconstructed.Lookup(ctx, bodySession.ID)
	if err != nil {
		t.Fatalf("Lookup closed session after restart: %v", err)
	}
	if persistedClosed.Status != session.StatusClosed {
		t.Fatalf("closed session reconstructed as %q", persistedClosed.Status)
	}
	if _, err := reconstructed.Resume(ctx, session.ResumeRequest{SessionID: bodySession.ID}); !errors.Is(err, session.ErrInvalidTransition) {
		t.Fatalf("Resume closed session after restart error = %v", err)
	}

	resumed, err := reconstructed.Resume(ctx, session.ResumeRequest{
		Owner:                   session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"},
		ExternalConversationRef: "conversation-one",
	})
	if err != nil {
		t.Fatalf("Resume Gateway session after restart: %v", err)
	}
	if resumed.ID != gatewaySession.ID {
		t.Fatalf("resumed session ID = %q, want %q", resumed.ID, gatewaySession.ID)
	}
}

func TestNewServicesRejectsMissingSessionStore(t *testing.T) {
	if _, err := runtime.NewServices(context.Background(), nil); err == nil {
		t.Fatal("NewServices(nil) succeeded")
	}
}

type runtimeInteractionHandler struct{ sessionID session.ID }

func (h *runtimeInteractionHandler) HandleInteraction(_ context.Context, resolved *session.InteractionSession, event *events.Event) (*events.Event, error) {
	h.sessionID = resolved.ID
	response := events.NewResponse(event.ID, event.DollID, "runtime-routed")
	return &response, nil
}

func TestRuntimeExposesSessionInteractionRouter(t *testing.T) {
	ctx := context.Background()
	services, store := openRuntimeServices(t, filepath.Join(t.TempDir(), "neondoll.db"))
	defer store.Close()
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}
	created, err := services.Sessions().Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: "conversation-one"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := &runtimeInteractionHandler{}
	router, err := services.InteractionRouter(handler)
	if err != nil {
		t.Fatalf("InteractionRouter: %v", err)
	}
	event := events.NewDollMessage("event-one", "doll-one", "hello")
	response, err := router.RouteInteraction(ctx, interaction.RouteRequest{SessionID: created.ID, Owner: owner, Event: &event})
	if err != nil {
		t.Fatalf("RouteInteraction: %v", err)
	}
	if handler.sessionID != created.ID || response.CorrelationID != event.ID {
		t.Fatalf("runtime route session = %q, response = %+v", handler.sessionID, response)
	}
}
