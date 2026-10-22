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

func newInteractionSessionSourceResolver(sessions session.Service, history HistoryStore) SourceResolver {
	return &interactionSessionSourceResolver{sessions: sessions, history: history}
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
			CreatedAt: message.CreatedAt,
		}
	}
	return resolved, nil
}
