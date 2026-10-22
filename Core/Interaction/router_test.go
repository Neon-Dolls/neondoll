package interaction_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

func openRouterSessions(t *testing.T) session.Service {
	t.Helper()
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionStore, ok := store.(session.Store)
	if !ok {
		t.Fatal("persistence store does not implement session.Store")
	}
	return session.New(sessionStore)
}

type recordingHandler struct {
	calls atomic.Int32
	mu    sync.Mutex
	seen  map[string]session.ID
}

func (h *recordingHandler) HandleInteraction(_ context.Context, resolved *session.InteractionSession, event *events.Event) (*events.Event, error) {
	h.calls.Add(1)
	h.mu.Lock()
	if h.seen != nil {
		h.seen[event.ID] = resolved.ID
	}
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, "routed")
	return &response, nil
}

func TestRouterResolvesAuthorizedActiveSession(t *testing.T) {
	ctx := context.Background()
	sessions := openRouterSessions(t)
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}
	created, err := sessions.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: "chat-one"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := &recordingHandler{seen: make(map[string]session.ID)}
	router, err := interaction.NewRouter(sessions, handler)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	event := events.NewDollMessage("event-one", "doll-one", "hello")
	response, err := router.RouteInteraction(ctx, interaction.RouteRequest{SessionID: created.ID, Owner: owner, Event: &event})
	if err != nil {
		t.Fatalf("RouteInteraction: %v", err)
	}
	if response.CorrelationID != event.ID {
		t.Fatalf("response correlation = %q, want %q", response.CorrelationID, event.ID)
	}
	if handler.seen[event.ID] != created.ID {
		t.Fatalf("handler session = %q, want %q", handler.seen[event.ID], created.ID)
	}
	updated, err := sessions.Lookup(ctx, created.ID)
	if err != nil {
		t.Fatalf("Lookup routed session: %v", err)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("routed session update time moved backward: %v < %v", updated.UpdatedAt, created.UpdatedAt)
	}
}

func TestRouterRejectsInvalidUnknownUnauthorizedAndClosedSessions(t *testing.T) {
	ctx := context.Background()
	sessions := openRouterSessions(t)
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}
	created, err := sessions.Create(ctx, session.CreateRequest{Owner: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := &recordingHandler{}
	router, err := interaction.NewRouter(sessions, handler)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	event := events.NewDollMessage("event-one", "doll-one", "hello")

	tests := []struct {
		name    string
		request interaction.RouteRequest
		want    error
	}{
		{name: "invalid", request: interaction.RouteRequest{Owner: owner, Event: &event}, want: interaction.ErrInvalidRoute},
		{name: "unknown", request: interaction.RouteRequest{SessionID: "missing", Owner: owner, Event: &event}, want: interaction.ErrUnknownSession},
		{name: "unauthorized", request: interaction.RouteRequest{
			SessionID: created.ID, Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-two"}, Event: &event,
		}, want: interaction.ErrUnauthorizedSession},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := router.RouteInteraction(ctx, test.request); !errors.Is(err, test.want) {
				t.Fatalf("RouteInteraction error = %v, want %v", err, test.want)
			}
		})
	}
	if _, err := sessions.Close(ctx, created.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := router.RouteInteraction(ctx, interaction.RouteRequest{SessionID: created.ID, Owner: owner, Event: &event}); !errors.Is(err, interaction.ErrClosedSession) {
		t.Fatalf("closed RouteInteraction error = %v, want ErrClosedSession", err)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("rejected routes invoked handler %d times", handler.calls.Load())
	}
}

type concurrentHandler struct {
	arrived chan session.ID
	release chan struct{}
	mu      sync.Mutex
	seen    map[string]session.ID
}

func (h *concurrentHandler) HandleInteraction(ctx context.Context, resolved *session.InteractionSession, event *events.Event) (*events.Event, error) {
	h.arrived <- resolved.ID
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	h.seen[event.ID] = resolved.ID
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, string(resolved.ID))
	return &response, nil
}

func TestRouterConcurrentSessionIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions := openRouterSessions(t)
	owners := []session.Owner{
		{Kind: session.OwnerGateway, InstanceID: "gateway-one"},
		{Kind: session.OwnerBody, InstanceID: "body-one"},
	}
	created := make([]*session.InteractionSession, len(owners))
	for i, owner := range owners {
		value, err := sessions.Create(ctx, session.CreateRequest{Owner: owner})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		created[i] = value
	}
	handler := &concurrentHandler{arrived: make(chan session.ID, 2), release: make(chan struct{}), seen: make(map[string]session.ID)}
	router, err := interaction.NewRouter(sessions, handler)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	type result struct {
		response *events.Event
		err      error
	}
	results := make(chan result, 2)
	for i := range created {
		event := events.NewDollMessage("event-"+string(rune('a'+i)), "doll-one", "hello")
		go func(index int, event events.Event) {
			response, err := router.RouteInteraction(ctx, interaction.RouteRequest{
				SessionID: created[index].ID, Owner: owners[index], Event: &event,
			})
			results <- result{response: response, err: err}
		}(i, event)
	}

	arrived := map[session.ID]bool{}
	for range created {
		select {
		case id := <-handler.arrived:
			arrived[id] = true
		case <-ctx.Done():
			t.Fatal("session routes did not execute concurrently")
		}
	}
	close(handler.release)
	for range created {
		result := <-results
		if result.err != nil {
			t.Errorf("RouteInteraction: %v", result.err)
		}
	}
	if !arrived[created[0].ID] || !arrived[created[1].ID] {
		t.Fatalf("concurrent handlers saw sessions %+v", arrived)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.seen["event-a"] != created[0].ID || handler.seen["event-b"] != created[1].ID {
		t.Fatalf("event/session routing crossed: %+v", handler.seen)
	}
}

func TestNewRouterRejectsMissingDependencies(t *testing.T) {
	handler := &recordingHandler{}
	if _, err := interaction.NewRouter(nil, handler); !errors.Is(err, interaction.ErrInvalidRoute) {
		t.Fatalf("NewRouter(nil, handler) error = %v", err)
	}
}
