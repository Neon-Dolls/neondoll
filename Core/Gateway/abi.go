// Package gateway hosts Core-managed Gateway instances and defines their
// transport-neutral ABI. Gateway authentication is installation/runtime
// ownership by Core; event payload fields never select an instance identity.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

const (
	LegacySessionEventVersion     = 0
	StructuredSessionEventVersion = 1
	WASMEnvelopeVersion0          = 0
	WASMTypeSessionEvent          = "session.event"
)

var (
	ErrInvalidAdapter    = errors.New("gateway: invalid adapter")
	ErrAlreadyRegistered = errors.New("gateway: instance already registered")
	ErrUnavailable       = errors.New("gateway: instance unavailable")
	ErrUnsupportedABI    = errors.New("gateway: structured Session events unsupported")
	ErrUnsupportedOwner  = errors.New("gateway: unsupported Session owner")
	ErrHostNotConfigured = errors.New("gateway: host not configured")
)

// VerifiedSender is produced by a platform adapter after platform-level
// verification. Role, principal, and authority claims remain untrusted.
type VerifiedSender struct {
	Platform  string            `json:"platform"`
	SubjectID string            `json:"subject_id"`
	Claims    map[string]string `json:"claims,omitempty"`
}

// OutboundEnvelope deliberately uses the existing Telegram WASM host/guest
// envelope shape (v/type/payload). The payload is the canonical Core delivery;
// its DeliveryID remains the adapter deduplication key.
type OutboundEnvelope struct {
	Version uint32          `json:"v"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func NewOutboundEnvelope(delivery sessionevent.Delivery) (OutboundEnvelope, error) {
	payload, err := json.Marshal(delivery)
	if err != nil {
		return OutboundEnvelope{}, fmt.Errorf("gateway: encode Session delivery: %w", err)
	}
	return OutboundEnvelope{Version: WASMEnvelopeVersion0, Type: WASMTypeSessionEvent, Payload: payload}, nil
}

func (e OutboundEnvelope) DecodeDelivery() (sessionevent.Delivery, error) {
	if e.Version != WASMEnvelopeVersion0 || e.Type != WASMTypeSessionEvent || !json.Valid(e.Payload) {
		return sessionevent.Delivery{}, ErrInvalidAdapter
	}
	var delivery sessionevent.Delivery
	if err := json.Unmarshal(e.Payload, &delivery); err != nil || strings.TrimSpace(delivery.ID) == "" {
		return sessionevent.Delivery{}, ErrInvalidAdapter
	}
	return delivery, nil
}

type PlatformResponse struct {
	SessionID session.ID    `json:"session_id"`
	Event     *events.Event `json:"event,omitempty"`
}

// SessionRoute is the transport routing information Core has authorized for
// the Gateway instance to which an Ingress is bound. It deliberately carries
// no participant identity or authority.
type SessionRoute struct {
	SessionID               session.ID `json:"session_id"`
	ExternalConversationRef string     `json:"external_conversation_ref"`
}

// Ingress is bound by Host to one configured Gateway instance. Adapters never
// pass their own instance ID or claim another Gateway's Session ownership.
type Ingress interface {
	ResolvePlatformSession(context.Context, string) (*SessionRoute, error)
	SessionRoute(context.Context, session.ID) (*SessionRoute, error)
	HandleLegacy(context.Context, session.ID, VerifiedSender, *events.Event) (*events.Event, error)
	HandlePlatformMessage(context.Context, string, VerifiedSender, *events.Event) (*PlatformResponse, error)
	HandleSessionEvent(context.Context, VerifiedSender, sessionevent.Event) (*permissionrequest.Request, error)
}

// Adapter is the Core-managed Gateway ABI. Existing text-only Gateways use
// LegacySessionEventVersion and retain HandleLegacy support. Structured outbound and
// decision events require StructuredSessionEventVersion.
type Adapter interface {
	InstanceID() externalidentity.GatewayInstanceID
	SessionEventVersion() int
	Start(context.Context, Ingress) error
	Deliver(context.Context, OutboundEnvelope) error
	Stop(context.Context) error
}

type boundIngress struct {
	version  int
	gateway  externalidentity.GatewayInstanceID
	sessions session.Service
	ingress  *interaction.GatewayIngress
	replay   ReplayFunc
}

func (b *boundIngress) HandlePlatformMessage(ctx context.Context, conversationRef string, sender VerifiedSender, event *events.Event) (*PlatformResponse, error) {
	if event == nil {
		return nil, ErrHostNotConfigured
	}
	resolved, err := b.ResolvePlatformSession(ctx, conversationRef)
	if err != nil {
		return nil, err
	}
	response, err := b.HandleLegacy(ctx, resolved.SessionID, sender, event)
	if err != nil {
		return nil, err
	}
	return &PlatformResponse{SessionID: resolved.SessionID, Event: response}, nil
}

// ResolvePlatformSession creates or resumes the active Session owned by this
// authenticated Gateway instance. Adapters use it for structured inbound
// events that must not be interpreted as model input.
func (b *boundIngress) ResolvePlatformSession(ctx context.Context, conversationRef string) (*SessionRoute, error) {
	if b.sessions == nil || strings.TrimSpace(conversationRef) == "" {
		return nil, ErrHostNotConfigured
	}
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: string(b.gateway)}
	resolved, err := b.sessions.Resume(ctx, session.ResumeRequest{Owner: owner, ExternalConversationRef: conversationRef})
	if errors.Is(err, session.ErrNotFound) {
		resolved, err = b.sessions.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: conversationRef})
		if errors.Is(err, session.ErrConversationInUse) {
			resolved, err = b.sessions.Resume(ctx, session.ResumeRequest{Owner: owner, ExternalConversationRef: conversationRef})
		}
	}
	if err != nil {
		return nil, err
	}
	return &SessionRoute{SessionID: resolved.ID, ExternalConversationRef: resolved.ExternalConversationRef}, nil
}

// SessionRoute returns restart-safe transport routing only when the Session is
// active and still owned by this authenticated Gateway instance.
func (b *boundIngress) SessionRoute(ctx context.Context, sessionID session.ID) (*SessionRoute, error) {
	if b.sessions == nil || strings.TrimSpace(string(sessionID)) == "" {
		return nil, ErrHostNotConfigured
	}
	resolved, err := b.sessions.Lookup(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	want := session.Owner{Kind: session.OwnerGateway, InstanceID: string(b.gateway)}
	if resolved.Status != session.StatusActive || resolved.Owner != want || strings.TrimSpace(resolved.ExternalConversationRef) == "" {
		return nil, ErrUnavailable
	}
	return &SessionRoute{SessionID: resolved.ID, ExternalConversationRef: resolved.ExternalConversationRef}, nil
}

func (b *boundIngress) HandleLegacy(ctx context.Context, sessionID session.ID, sender VerifiedSender, event *events.Event) (*events.Event, error) {
	response, err := b.ingress.HandleVerifiedInteraction(ctx, sessionID, interaction.VerifiedExternalSender{
		Platform: sender.Platform, SubjectID: sender.SubjectID, Claims: sender.Claims,
	}, event)
	if err != nil {
		return nil, err
	}
	if err := b.replay(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (b *boundIngress) HandleSessionEvent(ctx context.Context, sender VerifiedSender, event sessionevent.Event) (*permissionrequest.Request, error) {
	if b.version < StructuredSessionEventVersion {
		return nil, ErrUnsupportedABI
	}
	request, err := b.ingress.HandleSessionEvent(ctx, interaction.VerifiedExternalSender{
		Platform: sender.Platform, SubjectID: sender.SubjectID, Claims: sender.Claims,
	}, event)
	if err != nil {
		return nil, err
	}
	if err := b.replay(ctx); err != nil {
		return nil, err
	}
	return request, nil
}
