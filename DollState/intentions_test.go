package dollstate

import (
	"encoding/json"
	"testing"
)

const (
	sparkIntentionID       = "intention-reconsider-existence"
	sparkIntentionWakeTime = "2026-10-01T12:00:00Z"
)

func TestIntentionAllCanonicalFields(t *testing.T) {
	it := IntentionItem{
		ID:          sparkIntentionID,
		Subject:     "Reconsider purpose and direction",
		Description: "Spark decided during L2 planning that this question deserves future attention.",
		WakeTime:    sparkIntentionWakeTime,
		State:       IntentionStatePending,
	}
	if it.ID != sparkIntentionID {
		t.Errorf("expected ID %q, got %q", sparkIntentionID, it.ID)
	}
	if it.Subject != "Reconsider purpose and direction" {
		t.Errorf("expected Subject %q, got %q", "Reconsider purpose and direction", it.Subject)
	}
	if it.Description == "" {
		t.Error("expected non-empty Description")
	}
	if it.WakeTime != sparkIntentionWakeTime {
		t.Errorf("expected WakeTime %q, got %q", sparkIntentionWakeTime, it.WakeTime)
	}
	if it.State != IntentionStatePending {
		t.Errorf("expected State %q, got %q", IntentionStatePending, it.State)
	}
}

func TestIntentionJSONRoundTrip(t *testing.T) {
	original := IntentionItem{
		ID:          sparkIntentionID,
		Subject:     "Reconsider purpose and direction",
		Description: "Spark decided this deserves future attention.",
		WakeTime:    sparkIntentionWakeTime,
		State:       IntentionStatePending,
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored IntentionItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != original.ID {
		t.Errorf("ID lost in round-trip: expected %q, got %q", original.ID, restored.ID)
	}
	if restored.Subject != original.Subject {
		t.Errorf("Subject lost in round-trip: expected %q, got %q", original.Subject, restored.Subject)
	}
	if restored.Description != original.Description {
		t.Errorf("Description lost in round-trip: expected %q, got %q", original.Description, restored.Description)
	}
	if restored.WakeTime != original.WakeTime {
		t.Errorf("WakeTime lost in round-trip: expected %q, got %q", original.WakeTime, restored.WakeTime)
	}
	if restored.State != original.State {
		t.Errorf("State lost in round-trip: expected %q, got %q", original.State, restored.State)
	}
}

func TestIntentionPendingExplicitlySerialized(t *testing.T) {
	it := IntentionItem{
		ID:       sparkIntentionID,
		Subject:  "Reconsider purpose",
		WakeTime: sparkIntentionWakeTime,
		State:    IntentionStatePending,
	}

	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal to map: %v", err)
	}

	if raw["state"] != "pending" {
		t.Errorf("expected state=\"pending\" in JSON, got %v", raw["state"])
	}

	// Verify WakeTime is present in JSON (not omitted or optional)
	if wt, ok := raw["wake_time"].(string); !ok || wt != sparkIntentionWakeTime {
		t.Errorf("expected wake_time=%q in JSON, got %v", sparkIntentionWakeTime, raw["wake_time"])
	}

	// Verify subject is present
	if g, ok := raw["subject"].(string); !ok || g != "Reconsider purpose" {
		t.Errorf("expected subject=\"Reconsider purpose\" in JSON, got %v", raw["subject"])
	}

	// Verify no non-portable runtime fields leak into JSON
	nonPortable := []string{"scheduler_id", "timer_handle", "goroutine_id", "queue_entry", "process_id"}
	for _, field := range nonPortable {
		if _, ok := raw[field]; ok {
			t.Errorf("non-portable runtime field %q leaked into Intention JSON", field)
		}
	}
}

func TestIntentionMinimalFields(t *testing.T) {
	// An Intention with only required fields should serialize and deserialize cleanly.
	it := IntentionItem{
		ID:       "intention-minimal",
		Subject:  "Minimal intention",
		WakeTime: "2026-10-01T00:00:00Z",
		State:    IntentionStatePending,
	}

	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored IntentionItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != it.ID {
		t.Errorf("ID: expected %q, got %q", it.ID, restored.ID)
	}
	if restored.Subject != it.Subject {
		t.Errorf("Subject: expected %q, got %q", it.Subject, restored.Subject)
	}
	if restored.WakeTime != it.WakeTime {
		t.Errorf("WakeTime: expected %q, got %q", it.WakeTime, restored.WakeTime)
	}
	if restored.State != IntentionStatePending {
		t.Errorf("State: expected %q, got %q", IntentionStatePending, restored.State)
	}
	if restored.Description != "" {
		t.Errorf("expected empty Description, got %q", restored.Description)
	}
}

func TestEmptyIntentionsValid(t *testing.T) {
	its := Intentions{}
	b, err := json.Marshal(its)
	if err != nil {
		t.Fatalf("json.Marshal empty Intentions: %v", err)
	}

	var restored Intentions
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal empty Intentions: %v", err)
	}

	if len(restored.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(restored.Items))
	}
}

func TestMultipleIntentionsCoexist(t *testing.T) {
	its := Intentions{
		Items: []IntentionItem{
			{
				ID:       "intention-first",
				Subject:  "First intention",
				WakeTime: "2026-10-01T00:00:00Z",
				State:    IntentionStatePending,
			},
			{
				ID:       "intention-second",
				Subject:  "Second intention",
				WakeTime: "2026-10-02T00:00:00Z",
				State:    IntentionStatePending,
			},
		},
	}

	b, err := json.Marshal(its)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored Intentions
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if len(restored.Items) != 2 {
		t.Fatalf("expected 2 intentions, got %d", len(restored.Items))
	}

	// Verify both survive in order (no implied ordering machinery)
	if restored.Items[0].ID != "intention-first" {
		t.Errorf("first intention ID: expected %q, got %q", "intention-first", restored.Items[0].ID)
	}
	if restored.Items[1].ID != "intention-second" {
		t.Errorf("second intention ID: expected %q, got %q", "intention-second", restored.Items[1].ID)
	}

	// Verify no ordering or scheduling fields leaked
	var raw map[string]any
	json.Unmarshal(b, &raw)
	nonPortable := []string{"priority", "index", "recurrence", "cron", "sort_order"}
	for _, field := range nonPortable {
		// Check inside items as well
		if items, ok := raw["items"].([]any); ok {
			for i, item := range items {
				if itemMap, ok := item.(map[string]any); ok {
					if _, exists := itemMap[field]; exists {
						t.Errorf("non-portable field %q leaked into Intention JSON at items[%d]", field, i)
					}
				}
			}
		}
	}
}

func TestIntentionsInDollStateRoundTrip(t *testing.T) {
	state := NewDollState()
	state.Identity = Identity{DollID: "test-doll", CanonicalName: "Tester"}
	state.Intentions = Intentions{
		Items: []IntentionItem{
			{
				ID:          sparkIntentionID,
				Subject:     "Reconsider purpose and direction",
				Description: "Spark decided this deserves future attention.",
				WakeTime:    sparkIntentionWakeTime,
				State:       IntentionStatePending,
			},
		},
	}

	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal DollState: %v", err)
	}

	var restored DollState
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal DollState: %v", err)
	}

	if len(restored.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention, got %d", len(restored.Intentions.Items))
	}
	if restored.Intentions.Items[0].ID != sparkIntentionID {
		t.Errorf("Intention ID: expected %q, got %q", sparkIntentionID, restored.Intentions.Items[0].ID)
	}
	if restored.Intentions.Items[0].Subject != "Reconsider purpose and direction" {
		t.Errorf("Intention Subject: expected %q, got %q", "Reconsider purpose and direction", restored.Intentions.Items[0].Subject)
	}
	if restored.Intentions.Items[0].Description != "Spark decided this deserves future attention." {
		t.Errorf("Intention Description mismatch")
	}
	if restored.Intentions.Items[0].WakeTime != sparkIntentionWakeTime {
		t.Errorf("Intention WakeTime: expected %q, got %q", sparkIntentionWakeTime, restored.Intentions.Items[0].WakeTime)
	}
	if restored.Intentions.Items[0].State != IntentionStatePending {
		t.Errorf("Intention State: expected %q, got %q", IntentionStatePending, restored.Intentions.Items[0].State)
	}

	// Verify the Intention contains no scheduler runtime fields
	var rawState map[string]any
	json.Unmarshal(b, &rawState)
	if intentions, ok := rawState["intentions"].(map[string]any); ok {
		if items, ok := intentions["items"].([]any); ok {
			for i, item := range items {
				if itemMap, ok := item.(map[string]any); ok {
					nonPortable := []string{"scheduler_id", "timer_handle", "goroutine_id", "queue_entry", "process_id", "db_row_id"}
					for _, field := range nonPortable {
						if _, exists := itemMap[field]; exists {
							t.Errorf("non-portable runtime field %q leaked at intentions.items[%d]", field, i)
						}
					}
				}
			}
		}
	}
}

func TestNewDollStateHasEmptyIntentions(t *testing.T) {
	state := NewDollState()
	// Zero-value Intentions{} has nil Items, which is a valid empty state.
	if len(state.Intentions.Items) != 0 {
		t.Errorf("expected zero Intentions.Items, got %d items", len(state.Intentions.Items))
	}
	// Verify the empty state round-trips cleanly in full DollState JSON.
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var restored DollState
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if restored.Intentions.Items != nil && len(restored.Intentions.Items) != 0 {
		t.Errorf("unexpected Intentions after round-trip empty state: %d items", len(restored.Intentions.Items))
	}
}
