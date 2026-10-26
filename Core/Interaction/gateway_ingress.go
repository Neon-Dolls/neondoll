package interaction

import (
	"context"
	"errors"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

var ErrUnauthenticatedGateway = errors.New("interaction: authenticated Gateway required")

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
	gateway  externalidentity.GatewayInstanceID
	resolver externalidentity.Resolver
	router   SessionRouter
}

func NewGatewayIngress(gateway externalidentity.GatewayInstanceID, resolver externalidentity.Resolver, router SessionRouter) (*GatewayIngress, error) {
	if strings.TrimSpace(string(gateway)) == "" || resolver == nil || router == nil {
		return nil, ErrInvalidRoute
	}
	return &GatewayIngress{gateway: gateway, resolver: resolver, router: router}, nil
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
	return i.router.RouteInteraction(ctx, RouteRequest{SessionID: sessionID, Owner: owner, Event: event})
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
