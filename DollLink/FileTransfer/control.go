package filetransfer

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── Control Message Types ───────────────────────────────────────────────

// ControlType identifies the kind of file-transfer control message.
type ControlType string

const (
	TypeOffer    ControlType = "file.offer"
	TypeAccept   ControlType = "file.accept"
	TypeReject   ControlType = "file.reject"
	TypeComplete ControlType = "file.complete"
	TypeReceived ControlType = "file.received"
	TypeCancel   ControlType = "file.cancel"
)

// ControlMessage is the generic JSON envelope for all file-transfer control
// messages, matching the spec wire format.
type ControlMessage struct {
	Type          ControlType `json:"type"`
	ID            string      `json:"id,omitempty"`
	Timestamp     time.Time   `json:"timestamp,omitempty"`
	BodyID        string      `json:"body_id,omitempty"`
	CorrelationID string      `json:"correlation_id,omitempty"`
	Payload       any         `json:"payload"`
}

// marshalPayload returns the payload encoded as json.RawMessage for marshalling.
func marshalPayload(p any) (json.RawMessage, error) {
	if p == nil {
		return nil, nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// MarshalJSON implements json.Marshaler — flattens payload into the same object.
func (m ControlMessage) MarshalJSON() ([]byte, error) {
	raw, err := marshalPayload(m.Payload)
	if err != nil {
		return nil, err
	}

	// Build a map with all fields, flattening payload fields into the top level.
	out := map[string]any{
		"type": m.Type,
	}
	if m.ID != "" {
		out["id"] = m.ID
	}
	if !m.Timestamp.IsZero() {
		out["timestamp"] = m.Timestamp
	}
	if m.BodyID != "" {
		out["body_id"] = m.BodyID
	}
	if m.CorrelationID != "" {
		out["correlation_id"] = m.CorrelationID
	}
	// Merge payload fields into the top-level JSON object.
	if raw != nil {
		pMap := make(map[string]any)
		if err := json.Unmarshal(raw, &pMap); err != nil {
			return nil, err
		}
		for k, v := range pMap {
			out[k] = v
		}
	}

	return json.Marshal(out)
}

// UnmarshalJSON implements json.Unmarshaler — dispatches payload to the
// correct struct based on the type field.
func (m *ControlMessage) UnmarshalJSON(data []byte) error {
	// First pass: extract envelope fields and the raw payload bytes.
	raw := struct {
		Type          ControlType     `json:"type"`
		ID            string          `json:"id,omitempty"`
		Timestamp     time.Time       `json:"timestamp,omitempty"`
		BodyID        string          `json:"body_id,omitempty"`
		CorrelationID string          `json:"correlation_id,omitempty"`
		Payload       json.RawMessage `json:"payload"`
	}{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	m.Type = raw.Type
	m.ID = raw.ID
	m.Timestamp = raw.Timestamp
	m.BodyID = raw.BodyID
	m.CorrelationID = raw.CorrelationID

	// If the JSON had a "payload" key, use it directly.
	if raw.Payload != nil {
		return m.decodePayload(raw.Type, raw.Payload)
	}

	// If there's no "payload" key, the payload fields may be flattened at top
	// level. Extract everything except envelope fields as the payload.
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	delete(all, "type")
	delete(all, "id")
	delete(all, "timestamp")
	delete(all, "body_id")
	delete(all, "correlation_id")
	delete(all, "payload")
	if len(all) == 0 {
		return nil // no payload
	}

	// Merge remaining fields into a single JSON object.
	payloadData, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return m.decodePayload(raw.Type, payloadData)
}

// decodePayload unmarshals raw payload bytes into the correct struct.
func (m *ControlMessage) decodePayload(typ ControlType, raw json.RawMessage) error {
	switch typ {
	case TypeOffer:
		var p Offer
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	case TypeAccept:
		var p Accept
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	case TypeReject:
		var p Reject
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	case TypeComplete:
		var p Complete
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	case TypeReceived:
		var p Received
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	case TypeCancel:
		var p Cancel
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		m.Payload = p
	default:
		return fmt.Errorf("filetransfer: unknown control message type %q", typ)
	}
	return nil
}

// ── Transfer State ──────────────────────────────────────────────────────

// TransferState tracks which lifecycle phase a transfer attempt is in.
type TransferState int

const (
	StateNone      TransferState = iota // no transfer known
	StateOffered                        // file.offer sent -> awaiting accept/reject
	StateActive                         // file.accept received -> transferring binary frames
	StateComplete                       // file.complete sent/received -> awaiting verification
	StateReceived                       // file.received sent -> terminal success
	StateCancelled                      // file.cancel or file.reject -> terminal failure
	StateFailed                         // protocol/io error -> terminal failure
)

// String returns a human-readable state name.
func (s TransferState) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateOffered:
		return "offered"
	case StateActive:
		return "active"
	case StateComplete:
		return "complete"
	case StateReceived:
		return "received"
	case StateCancelled:
		return "cancelled"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Transfer is the runtime state for one transfer attempt.
type Transfer struct {
	FileID     FileID        // immutable file identity
	TransferID TransferID    // this transfer attempt
	Size       int64         // expected total byte count
	SHA256     string        // expected SHA-256 hex
	State      TransferState // current lifecycle state
	Offset     int64         // bytes transferred so far (sender: sent, receiver: retained)
}

// ── State Machine ───────────────────────────────────────────────────────

// Transition represents a valid state change in the transfer lifecycle.
type Transition struct {
	From TransferState
	To   TransferState
	Via  ControlType // the control message that triggers the transition
}

// validTransitions enumerates all legal transitions.
var validTransitions = []Transition{
	{From: StateNone, To: StateOffered, Via: TypeOffer},
	{From: StateOffered, To: StateActive, Via: TypeAccept},
	{From: StateOffered, To: StateCancelled, Via: TypeReject},
	{From: StateOffered, To: StateCancelled, Via: TypeCancel},
	{From: StateActive, To: StateComplete, Via: TypeComplete},
	{From: StateActive, To: StateCancelled, Via: TypeCancel},
	{From: StateComplete, To: StateReceived, Via: TypeReceived},
	{From: StateComplete, To: StateCancelled, Via: TypeCancel},
	{From: StateComplete, To: StateFailed, Via: TypeCancel}, // verify failure
}

// terminalStates are the states where no further transitions are allowed.
var terminalStates = map[TransferState]bool{
	StateReceived:  true,
	StateCancelled: true,
	StateFailed:    true,
}

// IsTerminal returns true if the state is a terminal (no further transitions).
func (s TransferState) IsTerminal() bool {
	return terminalStates[s]
}

// CheckTransition returns an error if moving from `current` to `next` via
// `msgType` is not a valid transition.
func CheckTransition(current, next TransferState, msgType ControlType) error {
	if current.IsTerminal() {
		return fmt.Errorf("filetransfer: cannot transition from terminal state %s", current)
	}
	for _, t := range validTransitions {
		if t.From == current && t.To == next && t.Via == msgType {
			return nil
		}
	}
	return fmt.Errorf("filetransfer: invalid transition %s -> %s via %s", current, next, msgType)
}
