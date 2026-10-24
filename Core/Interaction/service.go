package interaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Service handles incoming Doll Link interactions by loading the addressed
// Doll from persistence and using inference to generate a response.
//
//	Doll Link ↔ Interaction ↔ Persistence + Inference
type Service struct {
	store    persistence.Store
	provider inference.Provider
	log      *logger.Logger
	builder  contextbuilder.Builder
	history  contextbuilder.HistoryStore
	sessions keyedLocks
	dolls    keyedLocks
}

var _ SessionHandler = (*Service)(nil)

// HandleInteraction is the session-aware entry point used by Router. The
// resolved Core Session ID selects durable history for a fresh bounded context;
// event fields never select or authorize the session.
func (s *Service) HandleInteraction(ctx context.Context, resolved *session.InteractionSession, event *events.Event) (*events.Event, error) {
	if resolved == nil || resolved.ID == "" || s.builder == nil || s.history == nil {
		return nil, fmt.Errorf("interaction session context unavailable")
	}
	release, err := s.sessions.acquire(ctx, string(resolved.ID))
	if err != nil {
		return nil, err
	}
	defer release()
	return s.handleInteraction(ctx, resolved.ID, event)
}

// New creates an Interaction service with the given persistence store and
// inference provider.
func New(store persistence.Store, provider inference.Provider, log *logger.Logger) *Service {
	service := &Service{
		store:    store,
		provider: provider,
		log:      log,
	}
	sessionStore, sessionOK := store.(session.Store)
	history, historyOK := store.(contextbuilder.HistoryStore)
	if sessionOK && historyOK {
		service.builder, _ = contextbuilder.New(session.New(sessionStore), history, nil)
		service.history = history
	}
	return service
}

// InteractionContextTokenBudget bounds every session inference input. A
// model-profile-specific budget remains a later Core 5 concern.
const InteractionContextTokenBudget = 8192

func (s *Service) handleInteraction(ctx context.Context, sessionID session.ID, event *events.Event) (*events.Event, error) {
	if event == nil {
		return nil, fmt.Errorf("nil event")
	}

	if event.DollID == "" {
		errResp := events.NewErrorResponse(event.ID, "", "doll_id is required")
		return &errResp, nil
	}

	currentInput, ok := interactionMessageText(event.Payload)
	if !ok {
		errResp := events.NewErrorResponse(event.ID, event.DollID, "valid message payload required")
		return &errResp, nil
	}
	turnID := session.NewTurnID()

	state, err := s.store.LoadDoll(ctx, event.DollID)
	if err != nil {
		if err == persistence.ErrDollNotFound {
			s.log.Info("doll not found", map[string]any{"doll_id": event.DollID})
			errResp := events.NewErrorResponse(event.ID, event.DollID, "doll not found")
			return &errResp, nil
		}
		return nil, fmt.Errorf("load doll: %w", err)
	}

	inferenceReq := inference.Request{Model: event.DollID, Temperature: 0.7}
	built, buildErr := s.builder.Build(ctx, contextbuilder.Request{
		SessionID:          sessionID,
		TokenBudget:        InteractionContextTokenBudget,
		SystemInstructions: interactionInstructions(state),
		RequiredMessages: []contextbuilder.Message{{
			Role: contextbuilder.RoleUser, Content: currentInput,
		}},
	})
	if buildErr != nil {
		s.log.Error("interaction context construction failed", map[string]any{"session_id": sessionID, "error": buildErr.Error()})
		errResp := events.NewErrorResponse(event.ID, event.DollID, "interaction context unavailable")
		return &errResp, nil
	}
	inferenceReq.Messages = make([]inference.Message, len(built.Messages))
	for index, message := range built.Messages {
		inferenceReq.Messages[index] = inference.Message{Role: string(message.Role), Content: message.Content, Provenance: message.Provenance}
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

	if _, err := s.history.AppendSessionMessages(ctx, sessionID, []contextbuilder.NewHistoryMessage{
		{TurnID: turnID, Role: contextbuilder.RoleUser, Content: currentInput},
		{TurnID: turnID, Role: contextbuilder.RoleAssistant, Content: result.Content},
	}); err != nil {
		s.log.Error("failed to persist interaction session history", map[string]any{"session_id": sessionID, "error": err.Error()})
		errResp := events.NewErrorResponse(event.ID, event.DollID, "failed to persist interaction")
		return &errResp, nil
	}

	// --- Persist interaction Memory ---
	releaseDoll, err := s.dolls.acquire(ctx, event.DollID)
	if err != nil {
		return nil, err
	}
	defer releaseDoll()
	state, err = s.store.LoadDoll(ctx, event.DollID)
	if err != nil {
		return nil, fmt.Errorf("reload doll for interaction memory: %w", err)
	}
	interactionID := uuid.New().String()
	nextSeq := nextSequence(state.Memories.Items)

	humanMemory := dollstate.MemoryItem{
		ID:            uuid.New().String(),
		InteractionID: interactionID,
		Kind:          dollstate.KindHumanMessage,
		Content:       currentInput,
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

func interactionInstructions(state *dollstate.DollState) []string {
	var parts []string
	if name := state.Identity.CanonicalName; name != "" {
		parts = append(parts, "Your name is "+name+".")
	}
	if soul := state.Soul.Content; soul != "" {
		parts = append(parts, "Your nature: "+soul)
	}
	if owner := state.Owner.Name; owner != "" {
		parts = append(parts, "Your owner is "+owner+".")
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, "\n")}
}

func interactionMessageText(payload any) (string, bool) {
	var text string
	switch p := payload.(type) {
	case events.MessagePayload:
		text = p.Text
	case map[string]any:
		var ok bool
		if text, ok = p["text"].(string); !ok {
			return "", false
		}
	default:
		return "", false
	}
	return text, strings.TrimSpace(text) != ""
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
