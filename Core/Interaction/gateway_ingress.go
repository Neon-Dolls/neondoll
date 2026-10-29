package interaction

import (
	"context"
	"errors"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

var ErrUnauthenticatedGateway = errors.New("interaction: authenticated Gateway required")

type PermissionDecisionHandler interface {
	Decide(context.Context, session.ID, externalidentity.Principal, sessionevent.PermissionDecidedPayload) (*permissionrequest.Request, error)
}

// VerifiedExternalSender is metadata established by a trusted platform
// adapter. Claims remain untrusted and cannot influence Core resolution.
type VerifiedExternalSender struct {
	Platform  string
	SubjectID string
	Claims    map[string]string
}

// GatewayIngress is bound to one authenticated Gateway instance by the Core
// runtime. Per-event input cannot select or override that instance identity.
type GatewayIngress struct {
	gateway      externalidentity.GatewayInstanceID
	resolver     externalidentity.Resolver
	participants session.TrustedParticipantService
	router       SessionRouter
	authorizer   SessionAuthorizer
	decisions    PermissionDecisionHandler
}

func NewGatewayIngress(gateway externalidentity.GatewayInstanceID, resolver externalidentity.Resolver, participants session.ParticipantService, router SessionRouter, decisions ...PermissionDecisionHandler) (*GatewayIngress, error) {
	if strings.TrimSpace(string(gateway)) == "" || resolver == nil || participants == nil || router == nil {
		return nil, ErrInvalidRoute
	}
	trustedParticipants, ok := participants.(session.TrustedParticipantService)
	if !ok {
		return nil, ErrInvalidRoute
	}
	ingress := &GatewayIngress{gateway: gateway, resolver: resolver, participants: trustedParticipants, router: router}
	if len(decisions) > 1 {
		return nil, ErrInvalidRoute
	}
	if len(decisions) == 1 {
		authorizer, ok := router.(SessionAuthorizer)
		if !ok {
			return nil, ErrInvalidRoute
		}
		ingress.authorizer = authorizer
		ingress.decisions = decisions[0]
	}
	return ingress, nil
}

// HandleSessionEvent accepts structured events only through the same trusted,
// Gateway-bound identity path as legacy interactions. This increment supports
// explicit permission decisions; other Core-inbound event types remain
// rejected until their processing semantics are implemented.
func (i *GatewayIngress) HandleSessionEvent(ctx context.Context, sender VerifiedExternalSender, event sessionevent.Event) (*permissionrequest.Request, error) {
	if i == nil || i.decisions == nil || strings.TrimSpace(string(i.gateway)) == "" {
		return nil, ErrInvalidRoute
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	payload, err := sessionevent.DecodePermissionDecision(event)
	if err != nil {
		return nil, err
	}
	principal, err := i.resolver.Resolve(ctx, externalidentity.PresentedIdentity{
		Identity: externalidentity.Identity{GatewayInstanceID: i.gateway, Platform: sender.Platform, SubjectID: sender.SubjectID}, Claims: sender.Claims,
	})
	if err != nil {
		return nil, permissionrequest.ErrUnauthorized
	}
	ctx = withResolvedPrincipal(ctx, principal)
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: string(i.gateway)}
	_, err = i.authorizer.AuthorizeSession(ctx, event.SessionID, owner, func(ctx context.Context, resolved *session.InteractionSession) error {
		_, bindErr := i.participants.BindResolvedParticipant(ctx, resolved.ID, session.ParticipantSource{SessionID: resolved.ID, GatewayInstanceID: string(i.gateway), Platform: sender.Platform, SubjectID: sender.SubjectID, PrincipalID: string(principal.ID)})
		return bindErr
	})
	if err != nil {
		return nil, err
	}
	return i.decisions.Decide(ctx, event.SessionID, principal, payload)
}

// HandleVerifiedInteraction resolves sender identity and routes through the
// existing session authority. SessionID is a routing selector only: Router
// independently verifies that the selected Session belongs to this Gateway.
func (i *GatewayIngress) HandleVerifiedInteraction(ctx context.Context, sessionID session.ID, sender VerifiedExternalSender, event *events.Event) (*events.Event, error) {
	if i == nil || strings.TrimSpace(string(i.gateway)) == "" {
		return nil, ErrUnauthenticatedGateway
	}
	if strings.TrimSpace(sender.Platform) == "" || strings.TrimSpace(sender.SubjectID) == "" || event == nil {
		return nil, ErrInvalidRoute
	}
	principal, err := i.resolver.Resolve(ctx, externalidentity.PresentedIdentity{
		Identity: externalidentity.Identity{GatewayInstanceID: i.gateway, Platform: sender.Platform, SubjectID: sender.SubjectID},
		Claims:   sender.Claims,
	})
	if err == nil {
		ctx = withResolvedPrincipal(ctx, principal)
	} else if !errors.Is(err, externalidentity.ErrUnmapped) {
		return nil, err
	}
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: string(i.gateway)}
	request := RouteRequest{SessionID: sessionID, Owner: owner, Event: event}
	if err == nil {
		request.onAuthorized = func(ctx context.Context, resolved *session.InteractionSession) error {
			_, bindErr := i.participants.BindResolvedParticipant(ctx, resolved.ID, session.ParticipantSource{SessionID: resolved.ID, GatewayInstanceID: string(i.gateway), Platform: sender.Platform, SubjectID: sender.SubjectID, PrincipalID: string(principal.ID)})
			return bindErr
		}
	}
	return i.router.RouteInteraction(ctx, request)
}

type resolvedPrincipalKey struct{}

func withResolvedPrincipal(ctx context.Context, principal externalidentity.Principal) context.Context {
	return context.WithValue(ctx, resolvedPrincipalKey{}, principal)
}

// ResolvedPrincipal returns the Core-configured principal attached by trusted
// Gateway ingress. An unmapped sender returns ok=false and has no authority.
func ResolvedPrincipal(ctx context.Context) (principal externalidentity.Principal, ok bool) {
	principal, ok = ctx.Value(resolvedPrincipalKey{}).(externalidentity.Principal)
	return principal, ok
}
