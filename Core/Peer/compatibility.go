package peer

import (
	"strings"
	"time"
)

// LegacyOwner is a compatibility input matching Core 5 portable Owner state.
// It is not a new persisted Core 6 representation.
type LegacyOwner struct {
	OwnerID string `json:"owner_id"`
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
}

// LegacyOwnerMigration preserves legacy prose as attributed source material;
// it does not make that prose directly editable relationship state.
type LegacyOwnerMigration struct {
	Peer            Identity     `json:"peer"`
	Relationship    Relationship `json:"relationship"`
	PrimaryEvent    PrimaryEvent `json:"primary_event"`
	LegacyProse     string       `json:"legacy_prose,omitempty"`
	ProseProvenance string       `json:"prose_provenance"`
}

// MigrateLegacyOwner creates a contract fixture only after strong
// re-verification. Deployment ownership, local administration, or possession
// of a legacy config file alone cannot designate a Primary Peer.
func MigrateLegacyOwner(owner LegacyOwner, at time.Time, assurance Assurance) (LegacyOwnerMigration, error) {
	id := ID(strings.TrimSpace(owner.OwnerID))
	if !validID(id) || at.IsZero() {
		return LegacyOwnerMigration{}, ErrInvalidIdentity
	}
	if err := RequireAssurance(assurance, OperationPrimaryPeerChange); err != nil {
		return LegacyOwnerMigration{}, err
	}
	event := PrimaryEvent{
		SchemaVersion: SchemaVersion,
		EventID:       "legacy-owner-establishment:" + string(id),
		Type:          PrimaryEstablish,
		At:            at,
		TieBreaker:    "legacy-owner-import",
		ActorKind:     ActorPeer,
		ActorPeerID:   id,
		PeerID:        id,
		Assurance:     assurance,
		Provenance:    "core5-owner-import:reverified",
	}
	if err := event.Validate(); err != nil {
		return LegacyOwnerMigration{}, err
	}
	return LegacyOwnerMigration{
		Peer:            Identity{SchemaVersion: SchemaVersion, ID: id, CreatedAt: at},
		Relationship:    Relationship{PeerID: id, RecognizedAt: at},
		PrimaryEvent:    event,
		LegacyProse:     owner.Content,
		ProseProvenance: "core5:owner/owner.md",
	}, nil
}
