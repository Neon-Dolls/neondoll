package self

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/DollState"
)

type Store interface {
	LoadSelf(context.Context, string) (dollstate.Self, error)
	ReplaceSelf(context.Context, string, string, dollstate.Self) (bool, error)
	SaveDoll(context.Context, *dollstate.DollState) error
}

type RevisionRequest struct {
	ExpectedRevisionID string
	Content            string
}

// Manager serializes process-local proposals while Store performs the atomic
// compare-and-swap that detects stale writers and protects the active revision.
type Manager struct {
	store     Store
	dollID    string
	maxTokens int
	now       func() time.Time
	mu        sync.Mutex
}

func NewManager(store Store, dollID string, maxTokens int) (*Manager, error) {
	if store == nil || strings.TrimSpace(dollID) == "" || maxTokens <= 0 {
		return nil, ErrInvalidConfiguration
	}
	return &Manager{store: store, dollID: dollID, maxTokens: maxTokens, now: time.Now}, nil
}

func (manager *Manager) Current(ctx context.Context) (dollstate.Self, error) {
	if err := ctx.Err(); err != nil {
		return dollstate.Self{}, err
	}
	return manager.store.LoadSelf(ctx, manager.dollID)
}

// SaveState serializes a whole-Doll save with Self replacement and refreshes
// the active Self under the same lock. This prevents an unrelated state save
// from restoring a stale Self snapshot after a concurrent revision.
func (manager *Manager) SaveState(ctx context.Context, state *dollstate.DollState) error {
	if state == nil || state.Identity.DollID != manager.dollID {
		return ErrInvalidConfiguration
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current, err := manager.store.LoadSelf(ctx, manager.dollID)
	if err != nil {
		return err
	}
	state.Self = current
	return manager.store.SaveDoll(ctx, state)
}

func (manager *Manager) Revise(ctx context.Context, request RevisionRequest) (dollstate.Self, error) {
	if err := ctx.Err(); err != nil {
		return dollstate.Self{}, err
	}
	content := strings.TrimSpace(request.Content)
	if content == "" {
		return dollstate.Self{}, ErrInvalidRevision
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()

	current, err := manager.store.LoadSelf(ctx, manager.dollID)
	if err != nil {
		return dollstate.Self{}, err
	}
	if current.RevisionID != request.ExpectedRevisionID {
		return dollstate.Self{}, ErrStaleRevision
	}
	now := manager.now().UTC()
	createdAt := current.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	next := dollstate.Self{
		RevisionID: uuid.NewString(), Revision: current.Revision + 1,
		CreatedAt: createdAt, UpdatedAt: now, Content: content,
		DisplayName: current.DisplayName, Pronouns: current.Pronouns, Tagline: current.Tagline,
	}
	if err := ValidateActive(next, manager.maxTokens); err != nil {
		return dollstate.Self{}, err
	}
	replaced, err := manager.store.ReplaceSelf(ctx, manager.dollID, request.ExpectedRevisionID, next)
	if err != nil {
		return dollstate.Self{}, err
	}
	if !replaced {
		return dollstate.Self{}, ErrStaleRevision
	}
	return next, nil
}

func IsStale(err error) bool { return errors.Is(err, ErrStaleRevision) }
