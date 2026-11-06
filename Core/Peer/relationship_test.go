package peer

import (
	"errors"
	"testing"
	"time"
)

func primaryEvent(id string, kind PrimaryEventType, at time.Time, peerID ID) PrimaryEvent {
	return PrimaryEvent{SchemaVersion: 1, EventID: id, Type: kind, At: at, TieBreaker: id, ActorKind: ActorPeer, ActorPeerID: peerID, PeerID: peerID, Assurance: AssuranceReverified, Provenance: "verified-test"}
}

func TestExactlyOnePrimaryPeerAndAuthorizedLifecycle(t *testing.T) {
	establish := primaryEvent("1", PrimaryEstablish, testTime, "peer-a")
	request := PrimaryEvent{SchemaVersion: 1, EventID: "2", Type: PrimaryReleaseRequest, At: testTime.Add(time.Minute), TieBreaker: "2", ActorKind: ActorDoll, PeerID: "peer-a", Assurance: AssuranceNone, Provenance: "doll-request"}
	grant := primaryEvent("3", PrimaryReleaseGrant, testTime.Add(2*time.Minute), "peer-a")
	state, err := ReplayPrimary([]PrimaryEvent{grant, establish, request})
	if err != nil || state.PeerID != "" {
		t.Fatalf("release: %+v %v", state, err)
	}
	second := primaryEvent("4", PrimaryEstablish, testTime.Add(time.Second), "peer-b")
	if _, err := ReplayPrimary([]PrimaryEvent{establish, second}); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("second primary: %v", err)
	}
}

func TestDollCannotGrantReleaseOrTransfer(t *testing.T) {
	release := primaryEvent("release", PrimaryReleaseGrant, testTime, "peer-a")
	release.ActorKind, release.ActorPeerID = ActorDoll, ""
	if err := release.Validate(); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("Doll release: %v", err)
	}
	transfer := primaryEvent("transfer", PrimaryTransfer, testTime, "peer-a")
	transfer.ActorKind, transfer.ActorPeerID, transfer.TargetPeerID = ActorDoll, "", "peer-b"
	if err := transfer.Validate(); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("Doll transfer: %v", err)
	}
}

func TestOnlyCurrentPrimaryCanTransfer(t *testing.T) {
	establish := primaryEvent("1", PrimaryEstablish, testTime, "peer-a")
	unauthorized := primaryEvent("2", PrimaryTransfer, testTime.Add(time.Minute), "peer-b")
	unauthorized.TargetPeerID = "peer-c"
	if _, err := ReplayPrimary([]PrimaryEvent{establish, unauthorized}); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("unauthorized transfer: %v", err)
	}
	authorized := primaryEvent("3", PrimaryTransfer, testTime.Add(time.Minute), "peer-a")
	authorized.TargetPeerID = "peer-b"
	state, err := ReplayPrimary([]PrimaryEvent{authorized, establish})
	if err != nil || state.PeerID != "peer-b" {
		t.Fatalf("transfer: %+v %v", state, err)
	}
}

func TestOnlyCurrentPrimaryCanGrantRelease(t *testing.T) {
	establish := primaryEvent("1", PrimaryEstablish, testTime, "peer-a")
	request := PrimaryEvent{SchemaVersion: 1, EventID: "2", Type: PrimaryReleaseRequest, At: testTime.Add(time.Minute), TieBreaker: "2", ActorKind: ActorDoll, PeerID: "peer-a", Assurance: AssuranceNone, Provenance: "doll-request"}
	grant := primaryEvent("3", PrimaryReleaseGrant, testTime.Add(2*time.Minute), "peer-b")
	if _, err := ReplayPrimary([]PrimaryEvent{establish, request, grant}); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("non-primary release grant: %v", err)
	}
	grant = primaryEvent("4", PrimaryReleaseGrant, testTime.Add(2*time.Minute), "peer-a")
	grant.Assurance = AssuranceAccount
	if err := grant.Validate(); !errors.Is(err, ErrInsufficientAssurance) {
		t.Fatalf("weak release assurance: %v", err)
	}
}

func TestOrdinaryRelationshipsRemainIndependent(t *testing.T) {
	relationships := []Relationship{{PeerID: "peer-a", RecognizedAt: testTime}, {PeerID: "peer-b", RecognizedAt: testTime}}
	if err := ValidateRelationships(relationships, PrimaryState{PeerID: "peer-a"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRelationships(relationships, PrimaryState{PeerID: "peer-c"}); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("unknown primary: %v", err)
	}
}
