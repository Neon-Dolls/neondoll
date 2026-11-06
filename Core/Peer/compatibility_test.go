package peer

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestLegacyOwnerCompatibilityRequiresReverification(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy_owner.json")
	if err != nil {
		t.Fatal(err)
	}
	var owner LegacyOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyOwner(owner, testTime, AssuranceAccount); !errors.Is(err, ErrInsufficientAssurance) {
		t.Fatalf("weak migration: %v", err)
	}
	migration, err := MigrateLegacyOwner(owner, testTime, AssuranceReverified)
	if err != nil {
		t.Fatal(err)
	}
	if migration.Peer.ID != "legacy-owner" || migration.PrimaryEvent.PeerID != migration.Peer.ID || migration.LegacyProse != owner.Content || migration.ProseProvenance == "" {
		t.Fatalf("migration: %+v", migration)
	}
}
