package peer

import (
	"strings"
	"time"
)

// RelationshipDescription is the Doll-authored portable understanding of a
// Peer relationship. It cannot mutate identity, Primary status, or authority.
type RelationshipDescription struct {
	SchemaVersion int       `json:"schema_version"`
	PeerID        ID        `json:"peer_id"`
	Revision      uint64    `json:"revision"`
	Content       string    `json:"content"`
	UpdatedAt     time.Time `json:"updated_at"`
	AuthoredBy    string    `json:"authored_by"`
	Provenance    string    `json:"provenance"`
}

func (d RelationshipDescription) Validate() error {
	if d.SchemaVersion != SchemaVersion || !validID(d.PeerID) || d.Revision == 0 || strings.TrimSpace(d.Content) == "" ||
		d.UpdatedAt.IsZero() || d.AuthoredBy != "doll" || strings.TrimSpace(d.Provenance) == "" {
		return ErrInvalidRelationship
	}
	return nil
}
