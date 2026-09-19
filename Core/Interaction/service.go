package interaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Service handles incoming Doll Link interactions by loading the addressed
// Doll from persistence and using inference to generate a response.
//
//   Doll Link ↔ Interaction ↔ Persistence + Inference
type Service struct {
	store    persistence.Store
	provider inference.Provider
	log      *logger.Logger
}

// New creates an Interaction service with the given persistence store and
// inference provider.
func New(store persistence.Store, provider inference.Provider, log *logger.Logger) *Service {
	return &Service{
		store:    store,
		provider: provider,
		log:      log,
	}
}

// HandleEvent processes an incoming event and returns a response.
//
// For a message event with a valid DollID, it loads the Doll from persistence,
// builds a minimal prompt from the loaded state, and calls the inference
// provider to generate a response.
//
// Unknown DollIDs return a clean error event. Nil state or missing ID return
// an error event. Inference failures produce a system error response.
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

	// Build a minimal prompt from the loaded state.
	prompt := buildPrompt(state, event)

	inferenceReq := inference.Request{
		Model: event.DollID,
		Messages: []inference.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.7,
	}

	result, err := s.provider.Infer(ctx, inferenceReq)
	if err != nil {
		s.log.Error("inference failed", map[string]any{
			"doll_id": event.DollID,
			"event":   event.ID,
			"error":   err.Error(),
		})
		errResp := events.NewErrorResponse(event.ID, event.DollID, "inference failed")
		return &errResp, nil
	}

	// --- Persist interaction Memory ---
	interactionID := uuid.New().String()
	nextSeq := nextSequence(state.Memories.Items)

	humanMemory := dollstate.MemoryItem{
		ID:            uuid.New().String(),
		InteractionID: interactionID,
		Kind:          dollstate.KindHumanMessage,
		Content:       extractMessageText(event.Payload),
		Sequence:      nextSeq,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}

	dollMemory := dollstate.MemoryItem{
		ID:            uuid.New().String(),
		InteractionID: interactionID,
		Kind:          dollstate.KindDollResponse,
		Content:       result.Content,
		Sequence:      nextSeq + 1,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}

	state.Memories.Items = append(state.Memories.Items, humanMemory, dollMemory)

	if err := s.store.SaveDoll(ctx, state); err != nil {
		s.log.Error("failed to persist interaction memory", map[string]any{
			"doll_id": event.DollID,
			"event":   event.ID,
			"error":   err.Error(),
		})
		errResp := events.NewErrorResponse(event.ID, event.DollID, "failed to persist interaction")
		return &errResp, nil
	}

	s.log.Info("interaction served and persisted", map[string]any{
		"doll_id":        event.DollID,
		"provider":       result.ProviderID,
		"tokens_used":    result.TokensUsed,
		"event":          event.ID,
		"interaction_id": interactionID,
	})

	response := events.NewResponse(event.ID, event.DollID, result.Content)
	return &response, nil
}

// buildPrompt constructs a minimal prompt from the loaded DollState.
//
// The prompt includes the Doll's identity and soul where available, and the
// user's message. This is intentionally minimal — no history, no memory
// retrieval, no elaborate templates.
func buildPrompt(state *dollstate.DollState, event *events.Event) string {
	var parts []string

	name := state.Identity.CanonicalName
	if name != "" {
		parts = append(parts, "Your name is "+name+".")
	}
	if soul := state.Soul.Content; soul != "" {
		parts = append(parts, "Your nature: "+soul)
	}
	if owner := state.Owner.Name; owner != "" {
		parts = append(parts, "Your owner is "+owner+".")
	}

	msgText := extractMessageText(event.Payload)
	if msgText != "" {
		parts = append(parts, msgText)
	}

	if len(parts) == 0 {
		return "Hello."
	}
	return strings.Join(parts, "\n")
}

func extractMessageText(payload any) string {
	switch p := payload.(type) {
	case events.MessagePayload:
		return p.Text
	case map[string]any:
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

// nextSequence returns the next monotonically increasing Sequence value
// for a new MemoryItem given the existing items.
func nextSequence(items []dollstate.MemoryItem) int {
	max := -1
	for _, item := range items {
		if item.Sequence > max {
			max = item.Sequence
		}
	}
	return max + 1
}