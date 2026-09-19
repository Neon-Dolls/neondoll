package interaction

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Service handles incoming Doll Link interactions by loading the addressed
// Doll from persistence and constructing a deterministic response.
//
// This is the minimal bridge between Doll Link and Core:
//   Doll Link ↔ Interaction ↔ Persistence
type Service struct {
	store persistence.Store
	log   *logger.Logger
}

// New creates an Interaction service with the given persistence store.
func New(store persistence.Store, log *logger.Logger) *Service {
	return &Service{store: store, log: log}
}

// HandleEvent processes an incoming event and returns a response.
//
// For a message event with a valid DollID, it loads the Doll from persistence
// and returns a deterministic response derived from the loaded DollState.
//
// Unknown DollIDs return a clean error event. Nil state or missing ID return
// an error event.
func (s *Service) HandleEvent(ctx context.Context, event *events.Event) (*events.Event, error) {
	if event == nil {
		return nil, fmt.Errorf("nil event")
	}

	if event.DollID == "" {
		errResp := events.NewErrorResponse(event.ID, "", "doll_id is required")
		return &errResp, nil
	}

	state, err := s.store.LoadDoll(ctx, event.DollID)
	if err != nil {
		if err == persistence.ErrDollNotFound {
			s.log.Info("doll not found", map[string]any{"doll_id": event.DollID})
			errResp := events.NewErrorResponse(event.ID, event.DollID, "doll not found")
			return &errResp, nil
		}
		return nil, fmt.Errorf("load doll: %w", err)
	}

	// Build a deterministic response from the loaded DollState.
	// This proves the interaction path resolved the correct persisted Doll.
	name := state.Identity.CanonicalName
	if name == "" {
		name = "Spark"
	}

	response := events.NewResponse(event.ID, event.DollID, fmt.Sprintf("Hello. I am %s.", name))
	s.log.Info("interaction served",
		map[string]any{
			"doll_id": event.DollID,
			"name":    name,
			"event":   event.ID,
		},
	)

	return &response, nil
}