package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

// Services is the process-local, transport-independent boundary through which
// Core components consume shared runtime services. It owns no transport or
// model context; durable resources remain owned by the daemon lifecycle.
type Services struct {
	sessions session.Service
}

// NewServices wires Core runtime services to existing durable storage. Reading
// active sessions during construction verifies that persisted session state is
// reconstructable before the running Core advertises service availability.
func NewServices(ctx context.Context, sessionStore session.Store) (*Services, error) {
	if sessionStore == nil {
		return nil, errors.New("runtime services: nil session store")
	}
	sessions := session.New(sessionStore)
	if _, err := sessions.ListActive(ctx, nil); err != nil {
		return nil, fmt.Errorf("runtime services: reconstruct sessions: %w", err)
	}
	return &Services{sessions: sessions}, nil
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
