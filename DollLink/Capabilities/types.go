// Package capabilities defines the canonical Body Protocol v1
// body.capabilities wire message.
package capabilities

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const Type = "body.capabilities"

// Capability is one advertised Body capability. Unknown IDs and metadata are
// intentionally preserved; advertisement never grants Core authority.
type Capability struct {
	ID          string            `json:"id"`
	Operations  []string          `json:"operations"`
	Available   bool              `json:"available"`
	Constraints map[string]string `json:"constraints,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type Payload struct {
	Capabilities []Capability `json:"capabilities"`
}

// Advertisement is the canonical common envelope for body.capabilities.
type Advertisement struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	BodyID    string    `json:"body_id"`
	Payload   Payload   `json:"payload"`
}

func NewAdvertisement(bodyID string, advertised []Capability) Advertisement {
	return Advertisement{
		Type: Type, ID: uuid.NewString(), Timestamp: time.Now().UTC(), BodyID: bodyID,
		Payload: Payload{Capabilities: advertised},
	}
}

// Validate checks structural invariants. The connection owner must separately
// compare BodyID with the authenticated session identity.
func (a Advertisement) Validate() error {
	if a.Type != Type {
		return fmt.Errorf("capabilities: type %q, want %q", a.Type, Type)
	}
	if a.ID == "" || a.BodyID == "" {
		return fmt.Errorf("capabilities: message id and body_id are required")
	}
	seen := make(map[string]struct{}, len(a.Payload.Capabilities))
	for _, capability := range a.Payload.Capabilities {
		if capability.ID == "" || len(capability.Operations) == 0 {
			return fmt.Errorf("capabilities: capability id and operations are required")
		}
		if _, exists := seen[capability.ID]; exists {
			return fmt.Errorf("capabilities: duplicate capability %q", capability.ID)
		}
		seen[capability.ID] = struct{}{}
		operations := make(map[string]struct{}, len(capability.Operations))
		for _, operation := range capability.Operations {
			if operation == "" {
				return fmt.Errorf("capabilities: empty operation for %q", capability.ID)
			}
			if _, exists := operations[operation]; exists {
				return fmt.Errorf("capabilities: duplicate operation %q for %q", operation, capability.ID)
			}
			operations[operation] = struct{}{}
		}
	}
	return nil
}
