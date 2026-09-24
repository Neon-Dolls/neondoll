package filetransfer

import (
	"encoding/json"
	"testing"
	"time"
)

func TestControlMessageJSON(t *testing.T) {
	t.Run("offer", func(t *testing.T) {
		payload := Offer{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
			Size:       4837281,
			SHA256:     "7e8cbe3d88da37ecc94ef62a08c23565cba0c6bfa6ea6cf79790f4e98336db3d",
			Name:       "spark.dollcard",
			MediaType:  "application/vnd.neondoll.dollcard",
			Purpose:    "attachment",
		}
		msg := ControlMessage{
			Type:      TypeOffer,
			ID:        "evt_100",
			Timestamp: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
			BodyID:    "body_desktop_1",
			Payload:   payload,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeOffer {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeOffer)
		}
		if decoded.ID != "evt_100" {
			t.Fatalf("id: got %q, want %q", decoded.ID, "evt_100")
		}
		if decoded.BodyID != "body_desktop_1" {
			t.Fatalf("body_id: got %q, want %q", decoded.BodyID, "body_desktop_1")
		}

		// Verify the payload decoded to the correct type
		p, ok := decoded.Payload.(Offer)
		if !ok {
			t.Fatalf("payload type: got %T, want Offer", decoded.Payload)
		}
		if p.FileID != "file_123" {
			t.Fatalf("file_id: got %q, want %q", p.FileID, "file_123")
		}
		if p.Size != 4837281 {
			t.Fatalf("size: got %d, want %d", p.Size, 4837281)
		}
		if p.TransferID.String() != "550e8400-e29b-41d4-a716-446655440000" {
			t.Fatalf("transfer_id: got %q, want %q", p.TransferID.String(), "550e8400-e29b-41d4-a716-446655440000")
		}
		if p.Name != "spark.dollcard" {
			t.Fatalf("name: got %q, want %q", p.Name, "spark.dollcard")
		}
	})

	t.Run("accept", func(t *testing.T) {
		payload := Accept{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
			Offset:     3145728,
		}
		msg := ControlMessage{
			Type:          TypeAccept,
			ID:            "evt_101",
			CorrelationID: "evt_100",
			Payload:       payload,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeAccept {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeAccept)
		}
		if decoded.CorrelationID != "evt_100" {
			t.Fatalf("correlation_id: got %q, want %q", decoded.CorrelationID, "evt_100")
		}

		p, ok := decoded.Payload.(Accept)
		if !ok {
			t.Fatalf("payload type: got %T, want Accept", decoded.Payload)
		}
		if p.Offset != 3145728 {
			t.Fatalf("offset: got %d, want %d", p.Offset, 3145728)
		}
	})

	t.Run("reject", func(t *testing.T) {
		payload := Reject{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
			Reason:     "insufficient_storage",
		}
		msg := ControlMessage{Type: TypeReject, ID: "evt_102", CorrelationID: "evt_100", Payload: payload}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeReject {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeReject)
		}

		p, ok := decoded.Payload.(Reject)
		if !ok {
			t.Fatalf("payload type: got %T, want Reject", decoded.Payload)
		}
		if p.Reason != "insufficient_storage" {
			t.Fatalf("reason: got %q, want %q", p.Reason, "insufficient_storage")
		}
	})

	t.Run("complete", func(t *testing.T) {
		payload := Complete{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
		}
		msg := ControlMessage{Type: TypeComplete, ID: "evt_150", Payload: payload}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeComplete {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeComplete)
		}

		p, ok := decoded.Payload.(Complete)
		if !ok {
			t.Fatalf("payload type: got %T, want Complete", decoded.Payload)
		}
		if p.FileID != "file_123" {
			t.Fatalf("file_id: got %q, want %q", p.FileID, "file_123")
		}
	})

	t.Run("received", func(t *testing.T) {
		payload := Received{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
		}
		msg := ControlMessage{Type: TypeReceived, ID: "evt_151", CorrelationID: "evt_150", Payload: payload}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeReceived {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeReceived)
		}

		p, ok := decoded.Payload.(Received)
		if !ok {
			t.Fatalf("payload type: got %T, want Received", decoded.Payload)
		}
		if p.FileID != "file_123" {
			t.Fatalf("file_id: got %q, want %q", p.FileID, "file_123")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		payload := Cancel{
			FileID:     "file_123",
			TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"),
			Reason:     "checksum_mismatch",
			Message:    "SHA-256 does not match expected value",
		}
		msg := ControlMessage{Type: TypeCancel, ID: "evt_160", Payload: payload}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded ControlMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != TypeCancel {
			t.Fatalf("type: got %q, want %q", decoded.Type, TypeCancel)
		}

		p, ok := decoded.Payload.(Cancel)
		if !ok {
			t.Fatalf("payload type: got %T, want Cancel", decoded.Payload)
		}
		if p.Reason != "checksum_mismatch" {
			t.Fatalf("reason: got %q, want %q", p.Reason, "checksum_mismatch")
		}
		if p.Message != "SHA-256 does not match expected value" {
			t.Fatalf("message: got %q, want %q", p.Message, "SHA-256 does not match expected value")
		}
	})

	t.Run("unknown_type", func(t *testing.T) {
		raw := `{"type":"file.unknown","payload":{"file_id":"f1"}}`
		var decoded ControlMessage
		err := json.Unmarshal([]byte(raw), &decoded)
		if err == nil {
			t.Fatal("expected error for unknown control type")
		}
	})
}

func TestTransferStateString(t *testing.T) {
	tests := []struct {
		state TransferState
		want  string
	}{
		{StateNone, "none"},
		{StateOffered, "offered"},
		{StateActive, "active"},
		{StateComplete, "complete"},
		{StateReceived, "received"},
		{StateCancelled, "cancelled"},
		{StateFailed, "failed"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestTransferStateIsTerminal(t *testing.T) {
	terminals := []TransferState{StateReceived, StateCancelled, StateFailed}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}
	nonTerminals := []TransferState{StateNone, StateOffered, StateActive, StateComplete}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}

func TestCheckTransition(t *testing.T) {
	t.Run("none -> offered via offer", func(t *testing.T) {
		if err := CheckTransition(StateNone, StateOffered, TypeOffer); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("offered -> active via accept", func(t *testing.T) {
		if err := CheckTransition(StateOffered, StateActive, TypeAccept); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("offered -> cancelled via reject", func(t *testing.T) {
		if err := CheckTransition(StateOffered, StateCancelled, TypeReject); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("active -> complete via complete", func(t *testing.T) {
		if err := CheckTransition(StateActive, StateComplete, TypeComplete); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("complete -> received via received", func(t *testing.T) {
		if err := CheckTransition(StateComplete, StateReceived, TypeReceived); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("received -> anything is invalid", func(t *testing.T) {
		if err := CheckTransition(StateReceived, StateNone, TypeCancel); err == nil {
			t.Fatalf("expected error for transition from terminal state")
		}
	})

	t.Run("none -> active (skip offer) invalid", func(t *testing.T) {
		if err := CheckTransition(StateNone, StateActive, TypeAccept); err == nil {
			t.Fatalf("expected error for invalid transition")
		}
	})

	t.Run("offered -> received invalid", func(t *testing.T) {
		if err := CheckTransition(StateOffered, StateReceived, TypeReceived); err == nil {
			t.Fatalf("expected error for invalid transition")
		}
	})
}
