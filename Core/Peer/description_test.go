package peer

import (
	"errors"
	"testing"
)

func TestRelationshipDescriptionIsDollAuthored(t *testing.T) {
	description := RelationshipDescription{SchemaVersion: 1, PeerID: "peer-a", Revision: 1, Content: "A trusted collaborator.", UpdatedAt: testTime, AuthoredBy: "doll", Provenance: "interaction:turn-1"}
	if err := description.Validate(); err != nil {
		t.Fatal(err)
	}
	description.AuthoredBy = "peer"
	if err := description.Validate(); !errors.Is(err, ErrInvalidRelationship) {
		t.Fatalf("external edit accepted: %v", err)
	}
}
