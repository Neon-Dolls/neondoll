package contextbuilder

import (
	"context"
	"errors"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

// InteractionSessionSource adapts the existing public session request to a
// source-neutral identity.
func InteractionSessionSource(id session.ID) ContextSource {
	return ContextSource{Kind: SourceInteractionSession, ID: string(id)}
}

type interactionSessionSourceResolver struct {
	sessions session.Service
	history  HistoryStore
}

// NewInteractionSessionSourceResolver adapts existing session lifecycle and
// history storage for composition with other source resolvers.
func NewInteractionSessionSourceResolver(sessions session.Service, history HistoryStore) (SourceResolver, error) {
	if sessions == nil || history == nil {
		return nil, ErrInvalidRequest
	}
	return &interactionSessionSourceResolver{sessions: sessions, history: history}, nil
}

// SourceMux routes each source kind to its dedicated resolver. The mux owns no
// records and does not permit fallback between source kinds.
type SourceMux struct {
	InteractionSessions SourceResolver
	CognitionRuns       SourceResolver
}

func (mux SourceMux) Resolve(ctx context.Context, source ContextSource) (ResolvedSource, error) {
	var resolver SourceResolver
	switch source.Kind {
	case SourceInteractionSession:
		resolver = mux.InteractionSessions
	case SourceCognitionRun:
		resolver = mux.CognitionRuns
	default:
		return ResolvedSource{}, ErrInvalidSource
	}
	if resolver == nil {
		return ResolvedSource{}, ErrSourceUnsupported
	}
	return resolver.Resolve(ctx, source)
}

func (resolver *interactionSessionSourceResolver) Resolve(ctx context.Context, source ContextSource) (ResolvedSource, error) {
	if source.Kind != SourceInteractionSession {
		return ResolvedSource{}, ErrSourceUnsupported
	}
	if strings.TrimSpace(source.ID) == "" {
		return ResolvedSource{}, ErrInvalidSource
	}
	sessionID := session.ID(source.ID)
	if _, err := resolver.sessions.Lookup(ctx, sessionID); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return ResolvedSource{}, errors.Join(ErrSourceNotFound, session.ErrNotFound)
		}
		return ResolvedSource{}, err
	}
	history, err := resolver.history.ListSessionMessages(ctx, sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return ResolvedSource{}, errors.Join(ErrSourceNotFound, session.ErrNotFound)
		}
		return ResolvedSource{}, err
	}
	resolved := ResolvedSource{Source: source, Records: make([]ContextRecord, len(history))}
	for index, message := range history {
		if message.SessionID != sessionID {
			return ResolvedSource{}, ErrSourceIsolation
		}
		resolved.Records[index] = ContextRecord{
			ID:        message.ID,
			Source:    source,
			Sequence:  message.Sequence,
			Role:      message.Role,
			Content:   message.Content,
			Required:  message.Role == RoleSystem,
			CreatedAt: message.CreatedAt,
		}
	}
	return resolved, nil
}
