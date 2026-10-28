package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

// telegramPR4Event matches contract.GatewayEventEnvelope in
// dividebyzero/neondoll-gateway-telegram PR #4. Conversion into trusted Core
// metadata belongs to the managed adapter, not to SessionEvent semantics.
type telegramPR4Event struct {
	EventVersion int    `json:"event_version"`
	EventType    string `json:"event_type"`
	Source       string `json:"source"`
	UpdateID     int64  `json:"update_id"`
	MessageID    int64  `json:"message_id"`
	ChatID       int64  `json:"chat_id"`
	SenderID     int64  `json:"sender_id"`
	Text         string `json:"text"`
}

type hostedAdapter struct {
	id      externalidentity.GatewayInstanceID
	version int
	mu      sync.Mutex
	ingress gateway.Ingress
	events  []gateway.OutboundEnvelope
	stopped bool
	deliver error
}

type gatewayRoutingIngress interface {
	ResolvePlatformSession(context.Context, string) (session.ID, error)
	ExternalConversationRef(context.Context, session.ID) (string, error)
}

func (a *hostedAdapter) InstanceID() externalidentity.GatewayInstanceID { return a.id }
func (a *hostedAdapter) SessionEventVersion() int                       { return a.version }
func (a *hostedAdapter) Start(_ context.Context, ingress gateway.Ingress) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ingress = ingress
	a.stopped = false
	return nil
}
func (a *hostedAdapter) Deliver(_ context.Context, event gateway.OutboundEnvelope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deliver != nil {
		return a.deliver
	}
	a.events = append(a.events, event)
	return nil
}
func (a *hostedAdapter) Stop(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	return nil
}
func (a *hostedAdapter) endpoint() gateway.Ingress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ingress
}
func (a *hostedAdapter) delivered() []gateway.OutboundEnvelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]gateway.OutboundEnvelope(nil), a.events...)
}

type gatewayInteractionHandler struct {
	mu        sync.Mutex
	principal externalidentity.Principal
}

func (h *gatewayInteractionHandler) HandleInteraction(ctx context.Context, _ *session.InteractionSession, event *events.Event) (*events.Event, error) {
	h.mu.Lock()
	h.principal, _ = interaction.ResolvedPrincipal(ctx)
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, "legacy-ok")
	return &response, nil
}

type gatewayFixture struct {
	store    persistence.Store
	services *runtime.Services
	runtime  *runtime.GatewayRuntime
	resolver *externalidentity.MemoryResolver
	handler  *gatewayInteractionHandler
	router   interaction.SessionRouter
	session  *session.InteractionSession
	database string
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	database := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-main", Platform: "test", SubjectID: "owner-external"}, Principal: externalidentity.Principal{ID: "owner-core", Authority: externalidentity.AuthorityOwner}},
		{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-main", Platform: "telegram", SubjectID: "12345"}, Principal: externalidentity.Principal{ID: "owner-core", Authority: externalidentity.AuthorityOwner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	services, err := runtime.NewServices(context.Background(), store.(session.Store), runtime.WithExternalIdentityResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	handler := &gatewayInteractionHandler{}
	router, err := services.InteractionRouter(handler)
	if err != nil {
		t.Fatal(err)
	}
	gatewayRuntime, err := runtime.NewGatewayRuntime(context.Background(), services, store.(sessionevent.Store),
		store.(permissionrequest.Store), resolver, router, actionpolicy.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gatewayRuntime.Shutdown(context.Background()) })
	created, err := services.Sessions().Create(context.Background(), session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-main"}, ExternalConversationRef: "conversation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &gatewayFixture{store: store, services: services, runtime: gatewayRuntime, resolver: resolver,
		handler: handler, router: router, session: created, database: database}
}

func (f *gatewayFixture) register(t *testing.T, version int) *hostedAdapter {
	t.Helper()
	adapter := &hostedAdapter{id: "gateway-main", version: version}
	if err := f.runtime.Register(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}
	return adapter
}

func (f *gatewayFixture) admitOwner(t *testing.T, adapter *hostedAdapter) {
	t.Helper()
	event := events.NewDollMessage("legacy-event", "doll-1", "hello")
	response, err := adapter.endpoint().HandleLegacy(context.Background(), f.session.ID,
		gateway.VerifiedSender{Platform: "test", SubjectID: "owner-external", Claims: map[string]string{"authority": "forged"}}, &event)
	if err != nil || response.CorrelationID != event.ID {
		t.Fatalf("legacy response=%#v error=%v", response, err)
	}
	if f.handler.principal.ID != "owner-core" || f.handler.principal.Authority != externalidentity.AuthorityOwner {
		t.Fatalf("resolved principal = %#v", f.handler.principal)
	}
}

func TestProductionGatewayRuntimeStructuredPermissionsAndLegacyCompatibility(t *testing.T) {
	f := newGatewayFixture(t)
	adapter := f.register(t, gateway.StructuredSessionEventVersion)
	f.admitOwner(t, adapter)

	request, delivery, err := f.runtime.CreatePermission(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-core", Origin: permissionrequest.OriginVoluntary,
		Question: "Proceed?", ExpiresAt: time.Now().Add(time.Hour),
		Correlation: permissionrequest.Correlation{SessionID: string(f.session.ID), TurnID: "turn-1"},
	})
	if err != nil || delivery.Status != sessionevent.DeliveryAccepted {
		t.Fatalf("permission request=%#v delivery=%#v error=%v", request, delivery, err)
	}
	delivered := adapter.delivered()
	if len(delivered) != 1 {
		t.Fatalf("outbound envelopes = %#v", delivered)
	}
	decoded, decodeErr := delivered[0].DecodeDelivery()
	if decodeErr != nil || delivered[0].Version != gateway.WASMEnvelopeVersion0 ||
		decoded.ID != delivery.ID || decoded.TargetPrincipalID != "owner-core" {
		t.Fatalf("outbound envelopes = %#v", delivered)
	}
	decision, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(request.ID), Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: string(request.ID)}, time.Now())
	approved, err := adapter.endpoint().HandleSessionEvent(context.Background(),
		gateway.VerifiedSender{Platform: "test", SubjectID: "owner-external"}, decision)
	if err != nil || approved.Status != permissionrequest.StatusApproved {
		t.Fatalf("approved=%#v error=%v", approved, err)
	}

	deniedRequest, _, err := f.runtime.CreatePermission(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-core", Origin: permissionrequest.OriginVoluntary,
		Question: "Delete?", ExpiresAt: time.Now().Add(time.Hour),
		Correlation: permissionrequest.Correlation{SessionID: string(f.session.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	denial, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: string(deniedRequest.ID), Decision: permissionrequest.DecisionDeny},
		sessionevent.Correlation{RequestID: string(deniedRequest.ID)}, time.Now())
	denied, err := adapter.endpoint().HandleSessionEvent(context.Background(),
		gateway.VerifiedSender{Platform: "test", SubjectID: "owner-external"}, denial)
	if err != nil || denied.Status != permissionrequest.StatusDenied {
		t.Fatalf("denied=%#v error=%v", denied, err)
	}
}

func TestTelegramPR4InboundEnvelopeUsesCoreOwnedSessionResolution(t *testing.T) {
	f := newGatewayFixture(t)
	adapter := f.register(t, gateway.StructuredSessionEventVersion)
	const fixture = `{"event_version":1,"event_type":"message.incoming","source":"telegram","update_id":42,"message_id":7,"chat_id":9001,"chat_type":"private","sender_id":12345,"sender_name":"Owner","text":"hello","received_at":1700000000}`
	var incoming telegramPR4Event
	if err := json.Unmarshal([]byte(fixture), &incoming); err != nil {
		t.Fatal(err)
	}
	if incoming.EventVersion != 1 || incoming.EventType != "message.incoming" {
		t.Fatalf("Telegram fixture = %#v", incoming)
	}
	sender := gateway.VerifiedSender{Platform: "telegram", SubjectID: fmt.Sprint(incoming.SenderID)}
	event := events.NewDollMessage(fmt.Sprintf("telegram:%d", incoming.UpdateID), "doll-1", incoming.Text)
	first, err := adapter.endpoint().HandlePlatformMessage(context.Background(), fmt.Sprintf("telegram/chat/%d", incoming.ChatID), sender, &event)
	if err != nil || first.SessionID == "" || first.Event.CorrelationID != event.ID {
		t.Fatalf("first platform response=%#v error=%v", first, err)
	}
	event2 := events.NewDollMessage("telegram:43", "doll-1", "again")
	second, err := adapter.endpoint().HandlePlatformMessage(context.Background(), fmt.Sprintf("telegram/chat/%d", incoming.ChatID), sender, &event2)
	if err != nil || second.SessionID != first.SessionID {
		t.Fatalf("second platform response=%#v error=%v", second, err)
	}
	resolved, err := f.services.Sessions().Lookup(context.Background(), first.SessionID)
	if err != nil || resolved.Owner.InstanceID != "gateway-main" || resolved.ExternalConversationRef != "telegram/chat/9001" {
		t.Fatalf("Core Session=%#v error=%v", resolved, err)
	}
}

func TestGatewayIngressResolvesAndAuthorizesTransportRoutes(t *testing.T) {
	f := newGatewayFixture(t)
	adapter := f.register(t, gateway.StructuredSessionEventVersion)
	routes, ok := adapter.endpoint().(gatewayRoutingIngress)
	if !ok {
		t.Fatal("bound Gateway ingress lacks routing extension")
	}

	created, err := routes.ResolvePlatformSession(context.Background(), "telegram/chat/9001")
	if err != nil {
		t.Fatal(err)
	}
	route, err := routes.ExternalConversationRef(context.Background(), created)
	if err != nil {
		t.Fatal(err)
	}
	if route != "telegram/chat/9001" {
		t.Fatalf("route = %#v", route)
	}

	foreign, err := f.services.Sessions().Create(context.Background(), session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-other"}, ExternalConversationRef: "telegram/chat/42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routes.ExternalConversationRef(context.Background(), foreign.ID); !errors.Is(err, gateway.ErrUnavailable) {
		t.Fatalf("foreign route error = %v", err)
	}
	if _, err := f.services.Sessions().Close(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	if _, err := routes.ExternalConversationRef(context.Background(), created); !errors.Is(err, gateway.ErrUnavailable) {
		t.Fatalf("closed route error = %v", err)
	}
}

func TestGatewayUnavailabilityAndRestartReplayDurableDelivery(t *testing.T) {
	f := newGatewayFixture(t)
	first := f.register(t, gateway.StructuredSessionEventVersion)
	f.admitOwner(t, first)
	if err := f.runtime.Unregister(context.Background(), "gateway-main"); err != nil {
		t.Fatal(err)
	}
	request, pending, err := f.runtime.CreatePermission(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-core", Origin: permissionrequest.OriginVoluntary,
		Question: "Retry after restart?", ExpiresAt: time.Now().Add(time.Hour),
		Correlation: permissionrequest.Correlation{SessionID: string(f.session.ID)},
	})
	if !errors.Is(err, gateway.ErrUnavailable) || pending.Status != sessionevent.DeliveryPending {
		t.Fatalf("pending request=%#v delivery=%#v error=%v", request, pending, err)
	}
	if err := f.runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	services, err := runtime.NewServices(context.Background(), f.store.(session.Store), runtime.WithExternalIdentityResolver(f.resolver))
	if err != nil {
		t.Fatal(err)
	}
	handler := &gatewayInteractionHandler{}
	router, _ := services.InteractionRouter(handler)
	restarted, err := runtime.NewGatewayRuntime(context.Background(), services, f.store.(sessionevent.Store),
		f.store.(permissionrequest.Store), f.resolver, router, actionpolicy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(context.Background())
	replacement := &hostedAdapter{id: "gateway-main", version: gateway.StructuredSessionEventVersion}
	if err := restarted.Register(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	delivered := replacement.delivered()
	if len(delivered) != 1 {
		t.Fatalf("replayed envelopes = %#v", delivered)
	}
	decoded, decodeErr := delivered[0].DecodeDelivery()
	if decodeErr != nil || decoded.ID != pending.ID || decoded.Event.ID != pending.Event.ID {
		t.Fatalf("replayed envelopes = %#v", delivered)
	}
	stored, err := restarted.Events().GetDelivery(context.Background(), pending.ID)
	if err != nil || stored.Status != sessionevent.DeliveryAccepted {
		t.Fatalf("stored delivery=%#v error=%v", stored, err)
	}
}

func TestPendingPermissionPresentsWhenOwnerSessionBecomesEligible(t *testing.T) {
	f := newGatewayFixture(t)
	adapter := f.register(t, gateway.StructuredSessionEventVersion)
	request, delivery, err := f.runtime.CreatePermission(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-core", Origin: permissionrequest.OriginVoluntary,
		Question: "Present after ingress?", ExpiresAt: time.Now().Add(time.Hour),
		Correlation: permissionrequest.Correlation{SessionID: string(f.session.ID)},
	})
	if !errors.Is(err, sessionevent.ErrRecipientUnavailable) || delivery != nil || request == nil {
		t.Fatalf("pre-ingress request=%#v delivery=%#v error=%v", request, delivery, err)
	}
	f.admitOwner(t, adapter)
	delivered := adapter.delivered()
	if len(delivered) != 1 {
		t.Fatalf("post-ingress deliveries = %#v", delivered)
	}
	decoded, decodeErr := delivered[0].DecodeDelivery()
	if decodeErr != nil || decoded.Event.Correlation.RequestID != string(request.ID) {
		t.Fatalf("post-ingress deliveries = %#v", delivered)
	}
}

func TestLegacyGatewayRejectsStructuredEventsWithoutBreakingText(t *testing.T) {
	f := newGatewayFixture(t)
	adapter := f.register(t, gateway.LegacySessionEventVersion)
	f.admitOwner(t, adapter)
	event, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypeStatus,
		sessionevent.StatusPayload{Status: "ready"}, sessionevent.Correlation{}, time.Now())
	delivery, err := f.runtime.Events().Emit(context.Background(), event, "owner-core")
	if !errors.Is(err, gateway.ErrUnsupportedABI) || delivery.Status != sessionevent.DeliveryFailed {
		t.Fatalf("legacy structured delivery=%#v error=%v", delivery, err)
	}
	decision, _ := sessionevent.NewEvent(f.session.ID, sessionevent.TypePermissionDecided,
		sessionevent.PermissionDecidedPayload{RequestID: "request", Decision: permissionrequest.DecisionApprove},
		sessionevent.Correlation{RequestID: "request"}, time.Now())
	if _, err := adapter.endpoint().HandleSessionEvent(context.Background(), gateway.VerifiedSender{Platform: "test", SubjectID: "owner-external"}, decision); !errors.Is(err, gateway.ErrUnsupportedABI) {
		t.Fatalf("legacy structured ingress error = %v", err)
	}
}
