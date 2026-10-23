package contextbuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Neon-Dolls/neondoll/DollState"
)

const recentCognitionMemories = 5

// MindState is the existing state-access subset of DollMind.MindAPI used by
// context construction. The resolver reads authoritative state on every build
// and never stores a duplicate DollState.
type MindState interface {
	State() *dollstate.DollState
}

type CognitionRun struct {
	ID           string
	Objective    string
	WakeEvidence string
}

type CognitionWorkingRecord struct {
	Role       Role
	Content    string
	Provenance string
	Required   bool
}

// CognitionRunResolver adapts one active, ephemeral Cognition Run to the
// shared Context Builder. Only run-local working records are retained here;
// authoritative Mind state remains owned by MindAPI.
type CognitionRunResolver struct {
	source       ContextSource
	mind         MindState
	objective    string
	wakeEvidence string

	mu      sync.RWMutex
	working []CognitionWorkingRecord
}

var _ SourceResolver = (*CognitionRunResolver)(nil)

func NewCognitionRunResolver(run CognitionRun, mind MindState, working []CognitionWorkingRecord) (*CognitionRunResolver, error) {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.Objective) == "" || mind == nil {
		return nil, ErrInvalidSource
	}
	resolver := &CognitionRunResolver{
		source:       ContextSource{Kind: SourceCognitionRun, ID: run.ID},
		mind:         mind,
		objective:    run.Objective,
		wakeEvidence: run.WakeEvidence,
	}
	if err := resolver.Append(working...); err != nil {
		return nil, err
	}
	return resolver, nil
}

func (resolver *CognitionRunResolver) Source() ContextSource { return resolver.source }

// Append adds available working state for subsequent builds in this run. It
// does not persist transcript or mutate Doll Mind state.
func (resolver *CognitionRunResolver) Append(records ...CognitionWorkingRecord) error {
	if err := validateCognitionWorking(records); err != nil {
		return err
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.working = append(resolver.working, records...)
	return nil
}

// ReplaceWorking atomically replaces the ephemeral working set used by later
// builds. It allows an execution runtime to make a resolved tool exchange
// selectable while keeping the currently unresolved exchange required.
func (resolver *CognitionRunResolver) ReplaceWorking(records []CognitionWorkingRecord) error {
	if err := validateCognitionWorking(records); err != nil {
		return err
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.working = append(resolver.working[:0], records...)
	return nil
}

func validateCognitionWorking(records []CognitionWorkingRecord) error {
	for _, record := range records {
		if !validRole(record.Role) || record.Role == RoleSystem || strings.TrimSpace(record.Content) == "" {
			return ErrInvalidRequest
		}
	}
	return nil
}

func (resolver *CognitionRunResolver) Resolve(_ context.Context, source ContextSource) (ResolvedSource, error) {
	if source.Kind != SourceCognitionRun {
		return ResolvedSource{}, ErrSourceUnsupported
	}
	if source != resolver.source {
		return ResolvedSource{}, ErrSourceNotFound
	}
	state := resolver.mind.State()
	if state == nil {
		return ResolvedSource{}, fmt.Errorf("resolve cognition run: %w", ErrSourceNotFound)
	}

	resolver.mu.RLock()
	working := append([]CognitionWorkingRecord(nil), resolver.working...)
	resolver.mu.RUnlock()

	records, err := resolver.stateRecords(state)
	if err != nil {
		return ResolvedSource{}, err
	}
	for _, record := range working {
		provenance := record.Provenance
		if provenance == "" {
			provenance = "cognition_run.working"
		}
		records = append(records, ContextRecord{
			ID:         fmt.Sprintf("%s/working/%d", source.ID, len(records)),
			Source:     source,
			Sequence:   len(records),
			Role:       record.Role,
			Content:    record.Content,
			Provenance: provenance,
			Required:   record.Required,
		})
	}
	return ResolvedSource{Source: source, Records: records}, nil
}

func (resolver *CognitionRunResolver) stateRecords(state *dollstate.DollState) ([]ContextRecord, error) {
	type mindState struct {
		Soul   dollstate.Soul   `json:"soul"`
		Owner  dollstate.Owner  `json:"owner"`
		Self   dollstate.Self   `json:"self"`
		Drives dollstate.Drives `json:"drives"`
	}
	type essentialIdentity struct {
		DollID        string `json:"doll_id"`
		CanonicalName string `json:"canonical_name"`
	}

	memories := state.Memories.Items
	if len(memories) > recentCognitionMemories {
		memories = memories[len(memories)-recentCognitionMemories:]
	}
	sections := []struct {
		id         string
		heading    string
		provenance string
		value      any
	}{
		{id: "mind", heading: "Context data — authoritative Doll Mind state", provenance: "doll_mind.state", value: mindState{
			Soul: state.Soul, Owner: state.Owner, Self: state.Self, Drives: state.Drives,
		}},
		{id: "goals", heading: "Context data — authoritative Goals", provenance: "doll_mind.goals", value: state.Goals},
		{id: "intentions", heading: "Context data — authoritative Intentions", provenance: "doll_mind.intentions", value: state.Intentions},
		{id: "memory", heading: "Context data — recent Memory references", provenance: "doll_mind.memory", value: memories},
	}
	identity, err := json.Marshal(essentialIdentity{DollID: state.Identity.DollID, CanonicalName: state.Identity.CanonicalName})
	if err != nil {
		return nil, fmt.Errorf("resolve cognition run identity: %w", err)
	}
	records := []ContextRecord{{
		ID:         resolver.source.ID + "/identity",
		Source:     resolver.source,
		Sequence:   0,
		Role:       RoleSystem,
		Content:    "Doll identity:\n" + string(identity),
		Provenance: "doll.identity",
		Required:   true,
	}}
	for _, section := range sections {
		encoded, err := json.Marshal(section.value)
		if err != nil {
			return nil, fmt.Errorf("resolve cognition run %s: %w", section.id, err)
		}
		records = append(records, ContextRecord{
			ID:         resolver.source.ID + "/" + section.id,
			Source:     resolver.source,
			Sequence:   len(records),
			Role:       RoleSystem,
			Content:    section.heading + ":\n" + string(encoded),
			Provenance: section.provenance,
		})
	}
	records = append(records, ContextRecord{
		ID:         resolver.source.ID + "/objective",
		Source:     resolver.source,
		Sequence:   len(records),
		Role:       RoleSystem,
		Content:    "Cognition Run objective:\n" + resolver.objective,
		Provenance: "cognition_run.objective",
		Required:   true,
	})
	if resolver.wakeEvidence != "" {
		records = append(records, ContextRecord{
			ID:         resolver.source.ID + "/wake",
			Source:     resolver.source,
			Sequence:   len(records),
			Role:       RoleSystem,
			Content:    "Context data — Cognition Run wake evidence:\n" + resolver.wakeEvidence,
			Provenance: "cognition_run.wake_evidence",
		})
	}
	return records, nil
}
