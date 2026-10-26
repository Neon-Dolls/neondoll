package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

// Services is the process-local, transport-independent boundary through which
// Core components consume shared runtime services. It owns no transport or
// model context; durable resources remain owned by the daemon lifecycle.
type Services struct {
	sessions           session.Service
	externalIdentities externalidentity.Resolver
}

type ServicesOption func(*Services) error

func WithExternalIdentityResolver(resolver externalidentity.Resolver) ServicesOption {
	return func(services *Services) error {
		if resolver == nil {
			return errors.New("runtime services: nil external identity resolver")
		}
		services.externalIdentities = resolver
		return nil
	}
}

// NewServices wires Core runtime services to existing durable storage. Reading
// active sessions during construction verifies that persisted session state is
// reconstructable before the running Core advertises service availability.
func NewServices(ctx context.Context, sessionStore session.Store, options ...ServicesOption) (*Services, error) {
	if sessionStore == nil {
		return nil, errors.New("runtime services: nil session store")
	}
	sessions := session.New(sessionStore)
	if _, err := sessions.ListActive(ctx, nil); err != nil {
		return nil, fmt.Errorf("runtime services: reconstruct sessions: %w", err)
	}
	services := &Services{sessions: sessions}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("runtime services: nil option")
		}
		if err := option(services); err != nil {
			return nil, err
		}
	}
	return services, nil
}

// Sessions returns the Core-owned Interaction Session lifecycle service. A
// future Gateway host should depend on this interface, not on SQLite or the
// concrete session Manager.
func (s *Services) Sessions() session.Service { return s.sessions }

// InteractionRouter binds a session-aware interaction handler to the running
// Core's session authority. Authenticated Body/Gateway ingress supplies the
// trusted owner and session ID when routing an event.
func (s *Services) InteractionRouter(handler interaction.SessionHandler) (interaction.SessionRouter, error) {
	return interaction.NewRouter(s.sessions, handler)
}

// GatewayIngress binds the retained resolver and authenticated runtime
// Gateway identity to an Interaction router. Gateway events cannot supply or
// override the bound identity.
func (s *Services) GatewayIngress(gateway externalidentity.GatewayInstanceID, router interaction.SessionRouter) (*interaction.GatewayIngress, error) {
	if s == nil || s.externalIdentities == nil {
		return nil, errors.New("runtime services: external identity resolver unavailable")
	}
	return interaction.NewGatewayIngress(gateway, s.externalIdentities, router)
}
