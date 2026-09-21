// Package dispatch validates OutboundAction proposals from DollMind and
// maps them to canonical Doll Link actions at the Core boundary.
//
// The dispatcher is a motor control subsystem: it sits between the Doll Mind
// (semantic, no transport knowledge) and Doll Link (transport, no planning
// semantics). Validation is defense-in-depth — malformed kinds and empty
// content are rejected before reaching any Sender.
package dispatch

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
)

// Sender delivers validated outbound actions to the Doll Link transport.
// Implementations may write to WebSocket clients, enqueue messages, or
// delegate to a connection pool.
type Sender interface {
	// SendAction delivers one outbound action through the transport.
	// Implementations must be safe for concurrent use.
	SendAction(ctx context.Context, action actions.Action) error
}

// Logger abstracts structured logging for the dispatch package.
type Logger interface {
	Info(msg string, fields ...map[string]any)
	Warn(msg string, fields ...map[string]any)
	Error(msg string, fields ...map[string]any)
}

// Service validates, maps, and dispatches OutboundAction proposals.
// It implements dollmind.Dispatcher.
type Service struct {
	sender Sender
	log    Logger
}

// New creates a dispatcher Service that sends validated actions through
// the given Sender onto the Doll Link transport.
func New(sender Sender, log Logger) *Service {
	return &Service{sender: sender, log: log}
}

// Dispatch validates the OutboundAction kind and content, maps it to the
// corresponding actions.Action, and delivers it through the Sender.
//
// Supported Core 1 kinds:
//   - ActionKindSendText → actions.TypeSendMessage with SendMessagePayload
//
// Returns an error when:
//   - The action kind is unsupported at the Core boundary.
//   - The required content field is empty.
//   - The Sender returns a transport-level error.
func (s *Service) Dispatch(ctx context.Context, action dollmind.OutboundAction) error {
	switch action.Kind {
	case dollmind.ActionKindSendText:
		return s.dispatchSendText(ctx, action)
	default:
		return fmt.Errorf("dispatch: unsupported action kind %q", action.Kind)
	}
}

func (s *Service) dispatchSendText(ctx context.Context, action dollmind.OutboundAction) error {
	if action.Content == "" {
		return fmt.Errorf("dispatch: %q requires non-empty content", dollmind.ActionKindSendText)
	}

	dlAction := actions.New(
		"",
		actions.TypeSendMessage,
		"",
		actions.SendMessagePayload{Text: action.Content},
	)

	if err := s.sender.SendAction(ctx, dlAction); err != nil {
		return fmt.Errorf("dispatch send: %w", err)
	}

	s.log.Info("outbound action dispatched", map[string]any{
		"kind":    string(action.Kind),
		"content": action.Content,
	})

	return nil
}