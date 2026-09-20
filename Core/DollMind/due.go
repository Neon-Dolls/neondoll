// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// Phase 1 — Spark Wakes Herself: Core can recogniser when a pending
// Intention's canonical WakeTime is due against an injectable reference
// time. This is purely deterministic recognition — no state mutation,
// no timers, no wake events, no cognition dispatch.

package dollmind

import (
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// DueIntentions returns all pending Intentions whose canonical WakeTime
// is not strictly in the future relative to the scheduler's reference time.
//
// An Intention is due when its WakeTime is equal to or earlier than the
// reference time. This is purely deterministic recognition — no state
// mutation, no timers, no cognition dispatch.
//
// Malformed wake times produce an explicit error identifying the
// malformed Intention. Consumed (completed) Intentions are silently
// excluded. The returned slice is nil when no Intentions are due.
func (s *Scheduler) DueIntentions() ([]dollstate.IntentionItem, error) {
	if s.mindAPI == nil {
		return nil, fmt.Errorf("mind API not set: cannot access state")
	}

	state := s.mindAPI.State()
	now := s.timeProvider()

	var due []dollstate.IntentionItem
	for _, item := range state.Intentions.Items {
		if item.State != dollstate.IntentionStatePending {
			continue
		}
		wakeTime, err := time.Parse(time.RFC3339, item.WakeTime)
		if err != nil {
			return nil, fmt.Errorf("due intentions: intention %q has invalid wake_time %q: %w",
				item.ID, item.WakeTime, err)
		}
		if !wakeTime.After(now) {
			due = append(due, item)
		}
	}
	return due, nil
}