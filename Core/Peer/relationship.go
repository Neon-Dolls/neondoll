package peer

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Relationship struct {
	PeerID       ID        `json:"peer_id"`
	RecognizedAt time.Time `json:"recognized_at"`
}

type PrimaryEventType string

const (
	PrimaryEstablish      PrimaryEventType = "establish"
	PrimaryAffirm         PrimaryEventType = "affirm"
	PrimaryReleaseRequest PrimaryEventType = "release_request"
	PrimaryReleaseGrant   PrimaryEventType = "release_grant"
	PrimaryTransfer       PrimaryEventType = "transfer"
)

type ActorKind string

const (
	ActorDoll ActorKind = "doll"
	ActorPeer ActorKind = "peer"
)

// PrimaryEvent is the portable allegiance lifecycle record. Establishment,
// release grants, and transfers are protected operations. A Doll may request
// release but cannot grant it or transfer allegiance.
type PrimaryEvent struct {
	SchemaVersion int              `json:"schema_version"`
	EventID       string           `json:"event_id"`
	Type          PrimaryEventType `json:"type"`
	At            time.Time        `json:"at"`
	TieBreaker    string           `json:"tie_breaker"`
	ActorKind     ActorKind        `json:"actor_kind"`
	ActorPeerID   ID               `json:"actor_peer_id,omitempty"`
	PeerID        ID               `json:"peer_id"`
	TargetPeerID  ID               `json:"target_peer_id,omitempty"`
	Assurance     Assurance        `json:"assurance"`
	Provenance    string           `json:"provenance"`
}

func (e PrimaryEvent) Validate() error {
	if e.SchemaVersion != SchemaVersion || strings.TrimSpace(e.EventID) == "" || e.At.IsZero() ||
		strings.TrimSpace(e.TieBreaker) == "" || !validID(e.PeerID) || strings.TrimSpace(e.Provenance) == "" || e.Assurance.rank() < 0 {
		return ErrInvalidRelationship
	}
	if e.ActorKind == ActorPeer && !validID(e.ActorPeerID) {
		return ErrInvalidRelationship
	}
	if e.ActorKind == ActorDoll && e.ActorPeerID != "" {
		return ErrInvalidRelationship
	}
	switch e.Type {
	case PrimaryEstablish:
		if e.ActorKind != ActorPeer || e.ActorPeerID != e.PeerID || e.TargetPeerID != "" {
			return ErrInvalidRelationship
		}
		return RequireAssurance(e.Assurance, OperationPrimaryPeerChange)
	case PrimaryAffirm:
		if e.ActorKind != ActorPeer || e.ActorPeerID != e.PeerID || e.TargetPeerID != "" {
			return ErrInvalidRelationship
		}
	case PrimaryReleaseRequest:
		if e.ActorKind != ActorDoll || e.TargetPeerID != "" {
			return ErrInvalidRelationship
		}
	case PrimaryReleaseGrant:
		if e.ActorKind != ActorPeer || e.ActorPeerID != e.PeerID || e.TargetPeerID != "" {
			return ErrInvalidRelationship
		}
		return RequireAssurance(e.Assurance, OperationPrimaryPeerChange)
	case PrimaryTransfer:
		if e.ActorKind != ActorPeer || e.ActorPeerID != e.PeerID || !validID(e.TargetPeerID) || e.TargetPeerID == e.PeerID {
			return ErrInvalidRelationship
		}
		return RequireAssurance(e.Assurance, OperationPrimaryPeerChange)
	default:
		return ErrInvalidRelationship
	}
	return nil
}

type PrimaryState struct {
	PeerID           ID     `json:"peer_id,omitempty"`
	ReleaseRequested bool   `json:"release_requested"`
	LastEventID      string `json:"last_event_id,omitempty"`
}

// ReplayPrimary deterministically reconstructs the one Primary Peer state.
// Event order in an imported card does not affect the result.
func ReplayPrimary(events []PrimaryEvent) (PrimaryState, error) {
	ordered := append([]PrimaryEvent(nil), events...)
	for _, event := range ordered {
		if err := event.Validate(); err != nil {
			return PrimaryState{}, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].At.Before(ordered[j].At)
		}
		if ordered[i].TieBreaker != ordered[j].TieBreaker {
			return ordered[i].TieBreaker < ordered[j].TieBreaker
		}
		return ordered[i].EventID < ordered[j].EventID
	})
	seen := map[string]bool{}
	state := PrimaryState{}
	for _, event := range ordered {
		if seen[event.EventID] {
			return PrimaryState{}, fmt.Errorf("%w: duplicate event", ErrInvalidRelationship)
		}
		seen[event.EventID] = true
		switch event.Type {
		case PrimaryEstablish:
			if state.PeerID != "" {
				return PrimaryState{}, fmt.Errorf("%w: primary already established", ErrInvalidRelationship)
			}
			state = PrimaryState{PeerID: event.PeerID, LastEventID: event.EventID}
		case PrimaryAffirm:
			if state.PeerID != event.PeerID {
				return PrimaryState{}, fmt.Errorf("%w: affirmation by non-primary", ErrInvalidRelationship)
			}
			state.LastEventID = event.EventID
		case PrimaryReleaseRequest:
			if state.PeerID != event.PeerID {
				return PrimaryState{}, fmt.Errorf("%w: release request for non-primary", ErrInvalidRelationship)
			}
			state.ReleaseRequested, state.LastEventID = true, event.EventID
		case PrimaryReleaseGrant:
			if state.PeerID != event.PeerID || !state.ReleaseRequested {
				return PrimaryState{}, fmt.Errorf("%w: unauthorized release", ErrInvalidRelationship)
			}
			state = PrimaryState{LastEventID: event.EventID}
		case PrimaryTransfer:
			if state.PeerID != event.PeerID {
				return PrimaryState{}, fmt.Errorf("%w: unauthorized transfer", ErrInvalidRelationship)
			}
			state = PrimaryState{PeerID: event.TargetPeerID, LastEventID: event.EventID}
		}
	}
	return state, nil
}

// ValidateRelationships preserves flexible ordinary relationships while
// enforcing stable unique identities and at most one reconstructed Primary.
func ValidateRelationships(relationships []Relationship, primary PrimaryState) error {
	seen := map[ID]bool{}
	for _, relationship := range relationships {
		if !validID(relationship.PeerID) || relationship.RecognizedAt.IsZero() || seen[relationship.PeerID] {
			return ErrInvalidRelationship
		}
		seen[relationship.PeerID] = true
	}
	if primary.PeerID != "" && !seen[primary.PeerID] {
		return fmt.Errorf("%w: primary is not a recognized peer", ErrInvalidRelationship)
	}
	return nil
}
