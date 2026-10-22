package interaction_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type ingressHandler struct {
	legacyCalls atomic.Int32
	mu          sync.Mutex
	sessions    []session.ID
	owners      []session.Owner
	entered     chan session.ID
	release     chan struct{}
}

func (h *ingressHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	h.legacyCalls.Add(1)
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

func (h *ingressHandler) HandleInteraction(ctx context.Context, resolved *session.InteractionSession, event *events.Event) (*events.Event, error) {
	if h.entered != nil {
		h.entered <- resolved.ID
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	h.mu.Lock()
	h.sessions = append(h.sessions, resolved.ID)
	h.owners = append(h.owners, resolved.Owner)
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, "authenticated")
	return &response, nil
}

func newBodyIngress(t *testing.T, sessions session.Service, handler *ingressHandler) *interaction.BodyIngress {
	t.Helper()
	router, err := interaction.NewRouter(sessions, handler)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	ingress, err := interaction.NewBodyIngress(sessions, router, handler)
	if err != nil {
		t.Fatalf("NewBodyIngress: %v", err)
	}
	return ingress
}

func TestBodyIngressUsesAuthenticatedIdentityAndResumesAcrossReconnect(t *testing.T) {
	ctx := context.Background()
	sessions := openRouterSessions(t)
	handler := &ingressHandler{}
	ingress := newBodyIngress(t, sessions, handler)

	first := events.NewDollMessage("event-one", "doll-one", "hello")
	first.Source = "body-forged"
	first.Payload = events.MessagePayload{Text: "hello", UserID: "body-forged"}
	if _, err := ingress.HandleAuthenticatedEvent(ctx, "body-one", &first); err != nil {
		t.Fatalf("first HandleAuthenticatedEvent: %v", err)
	}
	second := events.NewDollMessage("event-two", "doll-one", "reconnected")
	if _, err := ingress.HandleAuthenticatedEvent(ctx, "body-one", &second); err != nil {
		t.Fatalf("reconnected HandleAuthenticatedEvent: %v", err)
	}
	third := events.NewDollMessage("event-three", "doll-one", "other Body")
	if _, err := ingress.HandleAuthenticatedEvent(ctx, "body-two", &third); err != nil {
		t.Fatalf("second Body HandleAuthenticatedEvent: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.sessions) != 3 {
		t.Fatalf("routed sessions = %v", handler.sessions)
	}
	if handler.sessions[0] != handler.sessions[1] {
		t.Fatalf("reconnect changed session: %q != %q", handler.sessions[0], handler.sessions[1])
	}
	if handler.sessions[0] == handler.sessions[2] {
		t.Fatalf("distinct Bodies shared session %q", handler.sessions[0])
	}
	if handler.owners[0] != (session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}) ||
		handler.owners[2] != (session.Owner{Kind: session.OwnerBody, InstanceID: "body-two"}) {
		t.Fatalf("authenticated owners = %+v", handler.owners)
	}
}

func TestBodyIngressRejectsMissingAuthenticatedIdentity(t *testing.T) {
	sessions := openRouterSessions(t)
	handler := &ingressHandler{}
	ingress := newBodyIngress(t, sessions, handler)
	event := events.NewDollMessage("event-one", "doll-one", "hello")
	if _, err := ingress.HandleAuthenticatedEvent(context.Background(), "", &event); !errors.Is(err, interaction.ErrUnauthenticatedBody) {
		t.Fatalf("HandleAuthenticatedEvent error = %v", err)
	}
	if len(handler.sessions) != 0 {
		t.Fatal("unauthenticated event reached handler")
	}
	active, err := sessions.ListActive(context.Background(), nil)
	if err != nil || len(active) != 0 {
		t.Fatalf("unauthenticated event created sessions: %+v, %v", active, err)
	}
}

func TestBodyIngressSessionClosureAndAdmittedInteractionSemantics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions := openRouterSessions(t)
	handler := &ingressHandler{entered: make(chan session.ID, 1), release: make(chan struct{})}
	ingress := newBodyIngress(t, sessions, handler)
	event := events.NewDollMessage("event-one", "doll-one", "hello")

	result := make(chan error, 1)
	go func() {
		_, err := ingress.HandleAuthenticatedEvent(ctx, "body-one", &event)
		result <- err
	}()
	var admittedID session.ID
	select {
	case admittedID = <-handler.entered:
	case <-ctx.Done():
		t.Fatal("interaction was not admitted")
	}
	if _, err := sessions.Close(ctx, admittedID); err != nil {
		t.Fatalf("Close admitted session: %v", err)
	}
	close(handler.release)
	if err := <-result; err != nil {
		t.Fatalf("already-admitted interaction failed after close: %v", err)
	}

	handler.entered = nil
	next := events.NewDollMessage("event-two", "doll-one", "new session")
	if _, err := ingress.HandleAuthenticatedEvent(ctx, "body-one", &next); err != nil {
		t.Fatalf("interaction after close: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.sessions) != 2 || handler.sessions[0] != admittedID || handler.sessions[1] == admittedID {
		t.Fatalf("session closure routing = %+v, admitted %q", handler.sessions, admittedID)
	}
}

func TestBodyIngressConcurrentFirstInteractionsShareBodySession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions := openRouterSessions(t)
	handler := &ingressHandler{}
	ingress := newBodyIngress(t, sessions, handler)

	const count = 24
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := events.NewDollMessage("concurrent", "doll-one", "hello")
			_, err := ingress.HandleAuthenticatedEvent(ctx, "body-one", &event)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("HandleAuthenticatedEvent: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.sessions) != count {
		t.Fatalf("routed interaction count = %d, want %d", len(handler.sessions), count)
	}
	first := handler.sessions[0]
	for _, routed := range handler.sessions[1:] {
		if routed != first {
			t.Fatalf("concurrent interactions crossed sessions: %q != %q", routed, first)
		}
	}
}
