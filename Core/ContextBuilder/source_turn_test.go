package contextbuilder

import (
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

func TestInteractionTurnGroupsPreferExplicitIDsAndIsolateLegacyFallback(t *testing.T) {
	history := []HistoryMessage{
		{ID: "legacy-user", Role: RoleUser},
		{ID: "legacy-assistant", Role: RoleAssistant},
		{ID: "explicit-user", TurnID: session.TurnID("turn-one"), Role: RoleUser},
		{ID: "legacy-orphan", Role: RoleAssistant},
		{ID: "explicit-tool", TurnID: session.TurnID("turn-one"), Role: RoleTool},
		{ID: "explicit-assistant", TurnID: session.TurnID("turn-one"), Role: RoleAssistant},
		{ID: "explicit-orphan", TurnID: session.TurnID("turn-two"), Role: RoleAssistant},
		{ID: "legacy-incomplete", Role: RoleUser},
	}
	groups := interactionTurnGroups(history)
	if groups[0].group == "" || groups[0].group != groups[1].group {
		t.Fatalf("legacy adjacent pair not grouped: %+v", groups[:2])
	}
	if groups[2].group == "" || groups[2].group != groups[4].group || groups[2].group != groups[5].group {
		t.Fatalf("explicit multi-record turn not grouped: %+v", groups[2:6])
	}
	if groups[3].omitReason != OmittedIncompleteTurn || groups[3].group != "" {
		t.Fatalf("legacy fallback crossed explicit turn boundary: %+v", groups[3])
	}
	if groups[6].omitReason != OmittedIncompleteTurn {
		t.Fatalf("explicit orphan assistant was selectable: %+v", groups[6])
	}
	if groups[7].group == "" || groups[7].omitReason != "" {
		t.Fatalf("incomplete legacy user handling changed: %+v", groups[7])
	}
}
