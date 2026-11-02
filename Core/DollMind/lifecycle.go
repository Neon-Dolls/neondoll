package dollmind

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

// Lifecycle serializes process-local cognition entry while leaving all
// authoritative state in DollState and PermissionRequest persistence. It is
// the production bridge shared by spontaneous Pulse wakes, durable intention
// wakes, and permission outcome reconsideration.
type Lifecycle struct {
	scheduler *Scheduler
	mu        sync.Mutex
}

func NewLifecycle(scheduler *Scheduler) (*Lifecycle, error) {
	if scheduler == nil {
		return nil, errors.New("dollmind lifecycle: scheduler is required")
	}
	return &Lifecycle{scheduler: scheduler}, nil
}

func (l *Lifecycle) EnterPulseWake(ctx context.Context, wake pulse.PulseWake) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scheduler.EnterPulseWake(ctx, wake)
}

// EnterDueIntentions discovers and dispatches all currently due durable
// intentions. Failed wakes remain pending through Scheduler.EnterWake and are
// retried on a later lifecycle tick. Other due intentions are still given a
// chance to run, and all failures remain observable to the caller.
func (l *Lifecycle) EnterDueIntentions(ctx context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	due, err := l.scheduler.DueIntentions()
	if err != nil {
		return 0, err
	}
	var failures []error
	for _, intention := range due {
		_, wakeErr := l.scheduler.EnterWake(ctx, events.IntentionWakePayload{
			IntentionID: intention.ID,
			Subject:     intention.Subject,
			Description: intention.Description,
		})
		if wakeErr != nil {
			failures = append(failures, fmt.Errorf("intention %q: %w", intention.ID, wakeErr))
		}
	}
	return len(due), errors.Join(failures...)
}

func (l *Lifecycle) NotifyPermissionResolution(ctx context.Context, notificationID string, request *permissionrequest.Request, outcome permissionrequest.ContinuationOutcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scheduler.NotifyPermissionResolution(ctx, notificationID, request, outcome)
}
