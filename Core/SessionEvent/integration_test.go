package sessionevent_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type recordingTransport struct {
	mu         sync.Mutex
	deliveries []sessionevent.Delivery
	owners     []session.Owner
	err        error
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (t *recordingTransport) DeliverSessionEvent(_ context.Context, owner session.Owner, delivery sessionevent.Delivery) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.owners = append(t.owners, owner)
	t.deliveries = append(t.deliveries, delivery)
	return t.err
}

func (t *recordingTransport) last() (sessionevent.Delivery, session.Owner) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deliveries[len(t.deliveries)-1], t.owners[len(t.owners)-1]
}

type echoHandler struct{}

func (echoHandler) HandleInteraction(_ context.Context, _ *session.InteractionSession, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "legacy response")
	return &response, nil
}

type eventFixture struct {
	store        persistence.Store
	sessions     *session.Manager
	resolver     *externalidentity.MemoryResolver
	requests     *permissionrequest.Manager
	delivery     *sessionevent.Service
	bridge       *sessionevent.PermissionBridge
	ingress      *interaction.GatewayIngress
	transport    *recordingTransport
	clock        *testClock
	session      *session.InteractionSession
	databasePath string
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := session.New(store.(session.Store))
	created, err := sessions.Create(context.Background(), session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}, ExternalConversationRef: "chat-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-one", Platform: "telegram", SubjectID: "owner-42"}, Principal: externalidentity.Principal{ID: "owner-zero", Authority: externalidentity.AuthorityOwner}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-one", Platform: "telegram", SubjectID: "member-7"}, Principal: externalidentity.Principal{ID: "member-seven", Authority: externalidentity.AuthorityParticipant}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-two", Platform: "telegram", SubjectID: "owner-42"}, Principal: externalidentity.Principal{ID: "other-owner", Authority: externalidentity.AuthorityOwner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &recordingTransport{}
	delivery, err := sessionevent.New(store.(sessionevent.Store), sessions, sessions, transport)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now().UTC()}
	requests := permissionrequest.NewWithClock(store.(permissionrequest.Store), actionpolicy.Default(), clock)
	bridge, err := sessionevent.NewPermissionBridge(requests, resolver, sessions, sessions, delivery)
	if err != nil {
		t.Fatal(err)
	}
	router, err := interaction.NewRouter(sessions, echoHandler{})
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := interaction.NewGatewayIngress("gateway-one", resolver, sessions, router, bridge)
	if err != nil {
		t.Fatal(err)
	}
	return &eventFixture{store: store, sessions: sessions, resolver: resolver, requests: requests, delivery: delivery,
		bridge: bridge, ingress: ingress, transport: transport, clock: clock, session: created, databasePath: databasePath}
}

func (f *eventFixture) admitOwner(t *testing.T) {
	t.Helper()
	event := events.NewDollMessage("legacy-1", "doll-1", "hello")
	response, err := f.ingress.HandleVerifiedInteraction(context.Background(), f.session.ID,
		interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, &event)
	if err != nil || response.CorrelationID != event.ID {
		t.Fatalf("legacy interaction response=%#v err=%v", response, err)
	}
}

func (f *eventFixture) createRequest(t *testing.T) *permissionrequest.Request {
	t.Helper()
	request, err := f.requests.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-zero", Origin: permissionrequest.OriginVoluntary,
		Question: "May I continue?", ExpiresAt: f.clock.Now().Add(time.Hour),
		Correlation: permissionrequest.Correlation{SessionID: string(f.session.ID), TurnID: "turn-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCoreInitiatedPermissionDeliveryAndOwnerDecisions(t *testing.T) {
	f := newEventFixture(t)
	f.admitOwner(t)
	request := f.createRequest(t)
	delivery, err := f.bridge.Present(context.Background(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != sessionevent.DeliveryAccepted || delivery.TargetPrincipalID != "owner-zero" {
		t.Fatalf("delivery = %#v", delivery)
	}
	got, owner := f.transport.last()
	if got.ID != delivery.ID || got.Event.ID == "" || owner != f.session.Owner || got.Event.Correlation.RequestID != string(request.ID) {
		t.Fatalf("transport delivery=%#v owner=%#v", got, owner)
	}

	decision, err := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(request.ID), Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: string(request.ID)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, decision)
	if err != nil || resolved.Status != permissionrequest.StatusApproved || resolved.ResolvingAuthority != "owner-zero" {
		t.Fatalf("approved request=%#v err=%v", resolved, err)
	}
	// Replaying the same authenticated decision is idempotent and never executes anything.
	replayed, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, decision)
	if err != nil || replayed.Status != permissionrequest.StatusApproved {
		t.Fatalf("replayed request=%#v err=%v", replayed, err)
	}
	conflict, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(request.ID), Decision: permissionrequest.DecisionDeny},
		decision.Correlation, time.Now())
	if _, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, conflict); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("conflicting decision error = %v", err)
	}

	deniedRequest := f.createRequest(t)
	denial, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(deniedRequest.ID), Decision: permissionrequest.DecisionDeny},
		sessionevent.Correlation{RequestID: string(deniedRequest.ID)}, time.Now())
	denied, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, denial)
	if err != nil || denied.Status != permissionrequest.StatusDenied {
		t.Fatalf("denied request=%#v err=%v", denied, err)
	}
}

func TestPermissionDecisionIsolationAndClosedSession(t *testing.T) {
	f := newEventFixture(t)
	f.admitOwner(t)
	request := f.createRequest(t)
	decision, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(request.ID), Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: string(request.ID)}, time.Now())
	for name, sender := range map[string]interaction.VerifiedExternalSender{
		"unmapped":   {Platform: "telegram", SubjectID: "unknown"},
		"wrong peer": {Platform: "telegram", SubjectID: "member-7"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.ingress.HandleSessionEvent(context.Background(), sender, decision); !errors.Is(err, permissionrequest.ErrUnauthorized) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := f.sessions.Close(context.Background(), f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bridge.Present(context.Background(), request.ID); !errors.Is(err, sessionevent.ErrRecipientUnavailable) {
		t.Fatalf("closed session delivery error = %v", err)
	}
	if _, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, decision); !errors.Is(err, interaction.ErrClosedSession) {
		t.Fatalf("closed session decision error = %v", err)
	}
}

func TestExpiredAndUnrelatedPermissionDecisionsAreRejected(t *testing.T) {
	f := newEventFixture(t)
	f.admitOwner(t)
	expired := f.createRequest(t)
	f.clock.advance(2 * time.Hour)
	decision, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(expired.ID), Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: string(expired.ID)}, time.Now())
	if _, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, decision); !errors.Is(err, permissionrequest.ErrExpired) {
		t.Fatalf("expired decision error = %v", err)
	}

	other, err := f.requests.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "other-owner", Origin: permissionrequest.OriginVoluntary,
		Question: "Other owner's question", ExpiresAt: f.clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(other.ID), Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: string(other.ID)}, time.Now())
	if _, err := f.ingress.HandleSessionEvent(context.Background(), interaction.VerifiedExternalSender{Platform: "telegram", SubjectID: "owner-42"}, unrelated); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("unrelated decision error = %v", err)
	}
}

func TestPendingDeliverySurvivesRestartAndReplaysWithStableIdentity(t *testing.T) {
	f := newEventFixture(t)
	f.admitOwner(t)
	event, err := sessionevent.NewEvent(f.session.ID, sessionevent.TypeStatus, sessionevent.StatusPayload{Status: "waiting"}, sessionevent.Correlation{RequestID: "request-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pending := &sessionevent.Delivery{ID: "delivery-stable", Event: event, TargetPrincipalID: "owner-zero", Owner: f.session.Owner,
		Status: sessionevent.DeliveryPending, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := f.store.(sessionevent.Store).CreateDelivery(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.NewStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sessions := session.New(reopened.(session.Store))
	transport := &recordingTransport{}
	service, err := sessionevent.New(reopened.(sessionevent.Store), sessions, sessions, transport)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ReplayPending(context.Background())
	if err != nil || len(replayed) != 1 || replayed[0].ID != pending.ID || replayed[0].Event.ID != event.ID || replayed[0].Status != sessionevent.DeliveryAccepted {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}
