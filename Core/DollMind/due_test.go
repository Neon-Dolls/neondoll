package dollmind

import (
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// ──────────────────────────────────────────────
// DueIntentions tests
// ──────────────────────────────────────────────

func refTime() time.Time {
	return time.Date(2035, 6, 15, 12, 0, 0, 0, time.UTC)
}

func pendingIntention(id, wakeTime, subject string) dollstate.IntentionItem {
	return dollstate.IntentionItem{
		ID:       id,
		Subject:  subject,
		WakeTime: wakeTime,
		State:    dollstate.IntentionStatePending,
	}
}

func completedIntention(id, wakeTime, subject string) dollstate.IntentionItem {
	return dollstate.IntentionItem{
		ID:       id,
		Subject:  subject,
		WakeTime: wakeTime,
		State:    dollstate.IntentionStateCompleted,
	}
}

func TestDueIntentions_FuturePending_NotDue(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						pendingIntention("i1", "2035-06-15T13:00:00Z", "reconsider direction"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected 0 due intentions, got %d", len(due))
	}
}

func TestDueIntentions_EqualTime_Due(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						pendingIntention("i1", "2035-06-15T12:00:00Z", "check progress"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due intention, got %d", len(due))
	}
	if due[0].ID != "i1" {
		t.Fatalf("expected intention i1, got %q", due[0].ID)
	}
	if due[0].Subject != "check progress" {
		t.Fatalf("expected subject 'check progress', got %q", due[0].Subject)
	}
	if due[0].WakeTime != "2035-06-15T12:00:00Z" {
		t.Fatalf("expected wake time '2035-06-15T12:00:00Z', got %q", due[0].WakeTime)
	}
}

func TestDueIntentions_PastPending_Due(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						pendingIntention("i1", "2035-06-15T11:00:00Z", "self-review"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due intention, got %d", len(due))
	}
	if due[0].ID != "i1" {
		t.Fatalf("expected intention i1, got %q", due[0].ID)
	}
}

func TestDueIntentions_Multiple_OnlyDueReturned(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						pendingIntention("i-future", "2035-06-15T13:00:00Z", "future"),
						pendingIntention("i-past", "2035-06-15T11:00:00Z", "past"),
						pendingIntention("i-now", "2035-06-15T12:00:00Z", "now"),
						pendingIntention("i-future2", "2035-06-15T14:00:00Z", "future2"),
						pendingIntention("i-past2", "2035-06-15T10:00:00Z", "past2"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 3 {
		t.Fatalf("expected 3 due intentions, got %d", len(due))
	}

	ids := make(map[string]bool)
	for _, d := range due {
		ids[d.ID] = true
	}
	if !ids["i-past"] {
		t.Errorf("expected i-past to be due, not found")
	}
	if !ids["i-now"] {
		t.Errorf("expected i-now to be due, not found")
	}
	if !ids["i-past2"] {
		t.Errorf("expected i-past2 to be due, not found")
	}
	if ids["i-future"] {
		t.Errorf("expected i-future to NOT be due, but it was")
	}
	if ids["i-future2"] {
		t.Errorf("expected i-future2 to NOT be due, but it was")
	}
}

func TestDueIntentions_Completed_NotDue(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						completedIntention("i1", "2035-06-15T11:00:00Z", "already done"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected 0 due intentions (completed), got %d", len(due))
	}
}

func TestDueIntentions_MalformedWakeTime_Error(t *testing.T) {
	s := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: []dollstate.IntentionItem{
						pendingIntention("i1", "not-a-real-time", "broken"),
					},
				},
			},
		},
		timeProvider: refTime,
	}

	_, err := s.DueIntentions()
	if err == nil {
		t.Fatal("expected error for malformed wake time, got nil")
	}
	if !strings.Contains(err.Error(), "i1") {
		t.Errorf("error should mention intention ID 'i1', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "not-a-real-time") {
		t.Errorf("error should mention the bad wake time, got: %s", err.Error())
	}
}

func TestDueIntentions_NoMutation(t *testing.T) {
	initial := []dollstate.IntentionItem{
		pendingIntention("i1", "2035-06-15T13:00:00Z", "future"),
		pendingIntention("i2", "2035-06-15T11:00:00Z", "past"),
	}

	sched := &Scheduler{
		mindAPI: &mockMindAPI{
			s: &dollstate.DollState{
				Intentions: dollstate.Intentions{
					Items: initial,
				},
			},
		},
		timeProvider: refTime,
	}

	_, err := sched.DueIntentions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := sched.mindAPI.State()
	if len(state.Intentions.Items) != 2 {
		t.Fatalf("expected 2 intentions after check, got %d", len(state.Intentions.Items))
	}
	if state.Intentions.Items[0].ID != "i1" {
		t.Errorf("intention 0 ID changed to %q", state.Intentions.Items[0].ID)
	}
	if state.Intentions.Items[1].ID != "i2" {
		t.Errorf("intention 1 ID changed to %q", state.Intentions.Items[1].ID)
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStatePending {
		t.Errorf("intention 0 state changed to %q", state.Intentions.Items[0].State)
	}
	if state.Intentions.Items[1].State != dollstate.IntentionStatePending {
		t.Errorf("intention 1 state changed to %q", state.Intentions.Items[1].State)
	}
}