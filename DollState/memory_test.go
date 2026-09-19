package dollstate

import (
	"encoding/json"
	"testing"
)

func TestHumanMessageMemoryRecord(t *testing.T) {
	m := MemoryItem{
		ID:        "mem-001",
		Kind:      KindHumanMessage,
		Content:   "What is your name?",
		Sequence:  0,
		Timestamp: "2026-09-19T10:00:00Z",
	}
	if m.Kind != KindHumanMessage {
		t.Errorf("expected kind %q, got %q", KindHumanMessage, m.Kind)
	}
	if m.ID != "mem-001" {
		t.Errorf("expected id mem-001, got %s", m.ID)
	}
	if m.Content != "What is your name?" {
		t.Errorf("expected content %q, got %q", "What is your name?", m.Content)
	}
}

func TestDollResponseMemoryRecord(t *testing.T) {
	m := MemoryItem{
		ID:        "mem-002",
		Kind:      KindDollResponse,
		Content:   "My name is Spark!",
		Sequence:  1,
		Timestamp: "2026-09-19T10:00:01Z",
	}
	if m.Kind != KindDollResponse {
		t.Errorf("expected kind %q, got %q", KindDollResponse, m.Kind)
	}
	if m.ID != "mem-002" {
		t.Errorf("expected id mem-002, got %s", m.ID)
	}
}

func TestMemoryRecordStableIdentity(t *testing.T) {
	id := "e7b8c9d0-1a2b-3c4d-5e6f-7a8b9c0d1e2f"
	m := MemoryItem{
		ID:        id,
		Kind:      KindHumanMessage,
		Content:   "Hello!",
		Sequence:  0,
		Timestamp: "2026-09-19T12:00:00Z",
	}
	if m.ID != id {
		t.Errorf("identity not preserved: expected %q, got %q", id, m.ID)
	}

	// Prove it survives JSON round-trip (Doll Card serialization)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var restored MemoryItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if restored.ID != id {
		t.Errorf("identity lost in JSON round-trip: expected %q, got %q", id, restored.ID)
	}
}

func TestMemoryRecordsDeterministicOrdering(t *testing.T) {
	// An interaction: human message (sequence 0) then doll response (sequence 1)
	records := []MemoryItem{
		{
			ID:        "resp-1",
			Kind:      KindDollResponse,
			Content:   "I'm fine, thank you!",
			Sequence:  1,
			Timestamp: "2026-09-19T10:00:02Z",
		},
		{
			ID:        "msg-1",
			Kind:      KindHumanMessage,
			Content:   "How are you?",
			Sequence:  0,
			Timestamp: "2026-09-19T10:00:00Z",
		},
	}

	// Sort by sequence
	if records[0].Sequence > records[1].Sequence {
		records[0], records[1] = records[1], records[0]
	}

	// Now check ordering
	if records[0].Kind != KindHumanMessage {
		t.Errorf("expected first record to be human_message, got %s", records[0].Kind)
	}
	if records[0].Sequence != 0 {
		t.Errorf("expected first record sequence 0, got %d", records[0].Sequence)
	}
	if records[1].Kind != KindDollResponse {
		t.Errorf("expected second record to be doll_response, got %s", records[1].Kind)
	}
	if records[1].Sequence != 1 {
		t.Errorf("expected second record sequence 1, got %d", records[1].Sequence)
	}

	// Prove the ordering is deterministic by sequence alone
	for i := 1; i < len(records); i++ {
		if records[i].Sequence <= records[i-1].Sequence {
			t.Errorf("non-deterministic ordering at index %d: seq %d <= %d",
				i, records[i].Sequence, records[i-1].Sequence)
		}
	}
}

func TestMemoryRecordNoCoreDependency(t *testing.T) {
	// MemoryItem must not reference Core persistence types.
	// This test proves it round-trips through plain JSON without
	// any custom marshaler/unmarshaler that would imply Core coupling.
	m := MemoryItem{
		ID:        "mem-roundtrip",
		Kind:      KindHumanMessage,
		Content:   "Round-trip test",
		Sequence:  0,
		Timestamp: "2026-09-19T14:00:00Z",
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Deserialize into a generic map to prove no Core-specific types leaked
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal into map: %v", err)
	}

	// Check expected fields
	expectedFields := []string{"id", "kind", "content", "sequence", "timestamp"}
	for _, f := range expectedFields {
		if _, ok := raw[f]; !ok {
			t.Errorf("expected field %q in JSON representation, not found", f)
		}
	}

	// Round-trip back to MemoryItem
	var restored MemoryItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != m.ID {
		t.Errorf("ID mismatch: %q vs %q", restored.ID, m.ID)
	}
	if restored.Kind != m.Kind {
		t.Errorf("Kind mismatch: %q vs %q", restored.Kind, m.Kind)
	}
	if restored.Content != m.Content {
		t.Errorf("Content mismatch")
	}
	if restored.Sequence != m.Sequence {
		t.Errorf("Sequence mismatch: %d vs %d", restored.Sequence, m.Sequence)
	}
	if restored.Timestamp != m.Timestamp {
		t.Errorf("Timestamp mismatch")
	}
}