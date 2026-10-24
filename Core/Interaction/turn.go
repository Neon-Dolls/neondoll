package interaction

import (
	"context"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type turnIDContextKey struct{}

func withTurnID(ctx context.Context, turnID session.TurnID) context.Context {
	return context.WithValue(ctx, turnIDContextKey{}, turnID)
}

// TurnIDFromContext returns the Core-owned external interaction identity. All
// processing and future inference steps admitted for one interaction share it.
func TurnIDFromContext(ctx context.Context) (session.TurnID, bool) {
	turnID, ok := ctx.Value(turnIDContextKey{}).(session.TurnID)
	return turnID, ok && turnID != ""
}
