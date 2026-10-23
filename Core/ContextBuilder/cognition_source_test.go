package contextbuilder_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollState"
)

type mindStateFixture struct{ state *dollstate.DollState }

func (fixture *mindStateFixture) State() *dollstate.DollState { return fixture.state }

var _ contextbuilder.MindState = (dollmind.MindAPI)(nil)

type oneTokenEstimator struct{}

func (oneTokenEstimator) EstimateTokens(contextbuilder.Message) int { return 1 }

func TestPulseCognitionRunBuildsWithoutInteractionSession(t *testing.T) {
	wake := pulse.PulseWake{
		AdmittedAt:        time.Date(2026, time.October, 23, 10, 0, 0, 0, time.UTC),
		Pressure:          0.8,
		EffectivePressure: 0.7,
		ActivationSignals: []float64{0.8},
		Subjects: []pulse.SubjectActivation{{
			SubjectID:  "goal-1",
			Unfinished: 1,
		}},
		RandomSample: 0.2,
	}
	mind := &mindStateFixture{state: cognitionDollState()}
	resolver, err := contextbuilder.NewCognitionRunResolver(contextbuilder.CognitionRun{
		ID:           "pulse-run-1",
		Objective:    "Orient to this spontaneous internal wake.",
		WakeEvidence: dollmind.FormatWakeEvidence(wake),
	}, mind, []contextbuilder.CognitionWorkingRecord{
		{Role: contextbuilder.RoleAssistant, Content: "first observation"},
		{Role: contextbuilder.RoleTool, Content: "available evidence"},
		{Role: contextbuilder.RoleAssistant, Content: "current thought"},
	})
	if err != nil {
		t.Fatal(err)
	}
	builder, err := contextbuilder.NewWithResolver(resolver, oneTokenEstimator{}, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	request := contextbuilder.SourceRequest{Source: resolver.Source(), TokenBudget: 9}
	first, err := builder.BuildSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != "" {
		t.Fatalf("Pulse cognition was attached to session %q", first.SessionID)
	}
	assertCognitionStateAvailable(t, first)
	if got := latestContents(first, 2); !reflect.DeepEqual(got, []string{"available evidence", "current thought"}) {
		t.Fatalf("initial working window = %v", got)
	}
	repeated, err := builder.BuildSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, repeated) {
		t.Fatalf("unchanged run produced nondeterministic context:\nfirst:  %+v\nsecond: %+v", first, repeated)
	}

	if err := resolver.Append(contextbuilder.CognitionWorkingRecord{Role: contextbuilder.RoleAssistant, Content: "advanced thought"}); err != nil {
		t.Fatal(err)
	}
	advanced, err := builder.BuildSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := latestContents(advanced, 2); !reflect.DeepEqual(got, []string{"current thought", "advanced thought"}) {
		t.Fatalf("advanced working window = %v", got)
	}
	if *advanced.Metadata.WindowStartSequence != *first.Metadata.WindowStartSequence+1 || *advanced.Metadata.WindowEndSequence != *first.Metadata.WindowEndSequence+1 {
		t.Fatalf("window did not advance: first=%+v advanced=%+v", first.Metadata, advanced.Metadata)
	}
}

func TestCognitionRunReadsCurrentAuthoritativeMindState(t *testing.T) {
	mind := &mindStateFixture{state: cognitionDollState()}
	resolver, err := contextbuilder.NewCognitionRunResolver(contextbuilder.CognitionRun{ID: "run-state", Objective: "Review goals."}, mind, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := contextbuilder.NewWithResolver(resolver, oneTokenEstimator{}, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	request := contextbuilder.SourceRequest{Source: resolver.Source(), TokenBudget: 10}
	before, err := builder.BuildSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mind.state.Goals.Items[0].Name = "Updated goal"
	after, err := builder.BuildSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joinContents(before), "Updated goal") || !strings.Contains(joinContents(after), "Updated goal") {
		t.Fatalf("builder did not read current authoritative state: before=%q after=%q", joinContents(before), joinContents(after))
	}
}

func TestCognitionRunLargeOptionalMindStateFitsSmallBudget(t *testing.T) {
	state := cognitionDollState()
	large := strings.Repeat("context ", 80)
	for index := range 100 {
		state.Goals.Items = append(state.Goals.Items, dollstate.GoalItem{
			ID:          "large-goal-" + strconv.Itoa(index),
			Name:        "Large goal",
			Description: large,
			State:       dollstate.GoalStateActive,
		})
		state.Intentions.Items = append(state.Intentions.Items, dollstate.IntentionItem{
			ID:          "large-intention-" + strconv.Itoa(index),
			Subject:     "Large intention",
			Description: large,
			State:       dollstate.IntentionStatePending,
		})
		state.Memories.Items = append(state.Memories.Items, dollstate.MemoryItem{
			ID:       "large-memory-" + strconv.Itoa(index),
			Kind:     dollstate.KindObservation,
			Content:  large,
			Sequence: index + 1,
		})
	}
	resolver, err := contextbuilder.NewCognitionRunResolver(
		contextbuilder.CognitionRun{ID: "large-run", Objective: "Review current state."},
		&mindStateFixture{state: state}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := contextbuilder.NewWithResolver(resolver, nil, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	small, err := builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: resolver.Source(), TokenBudget: 50})
	if err != nil {
		t.Fatalf("small bounded context failed: %v", err)
	}
	if small.Metadata.EstimatedTokens > 50 {
		t.Fatalf("small context estimate = %d", small.Metadata.EstimatedTokens)
	}
	if !hasProvenance(small.Messages, "doll.identity") || !hasProvenance(small.Messages, "cognition_run.objective") {
		t.Fatalf("small context omitted essentials: %+v", small.Messages)
	}
	for _, omitted := range small.Metadata.OmittedMessages {
		if omitted.Provenance == "doll_mind.goals" || omitted.Provenance == "doll_mind.intentions" || omitted.Provenance == "doll_mind.memory" {
			goto optionalOmitted
		}
	}
	t.Fatal("large optional Mind context was not reported as omitted")

optionalOmitted:
	largeResult, err := builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: resolver.Source(), TokenBudget: 100000})
	if err != nil {
		t.Fatal(err)
	}
	for _, provenance := range []string{"doll_mind.state", "doll_mind.goals", "doll_mind.intentions", "doll_mind.memory"} {
		if !hasProvenance(largeResult.Messages, provenance) {
			t.Errorf("large context omitted optional provenance %q", provenance)
		}
	}
}

func TestCognitionRunSourceIsolation(t *testing.T) {
	resolver, err := contextbuilder.NewCognitionRunResolver(contextbuilder.CognitionRun{ID: "run-one", Objective: "Think."}, &mindStateFixture{state: cognitionDollState()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "run-two"})
	if !errors.Is(err, contextbuilder.ErrSourceNotFound) {
		t.Fatalf("Resolve foreign run error = %v", err)
	}
}

func TestSourceMuxPreservesInteractionSessionCompatibility(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID, contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "session message"})
	sessionResolver, err := contextbuilder.NewInteractionSessionSourceResolver(fx.sessions, fx.history)
	if err != nil {
		t.Fatal(err)
	}
	cognitionResolver, err := contextbuilder.NewCognitionRunResolver(contextbuilder.CognitionRun{ID: "run", Objective: "Think."}, &mindStateFixture{state: cognitionDollState()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := contextbuilder.NewWithResolver(contextbuilder.SourceMux{
		InteractionSessions: sessionResolver,
		CognitionRuns:       cognitionResolver,
	}, contentLengthEstimator{}, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 20})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].Content != "session message" {
		t.Fatalf("session result = %+v, %v", result, err)
	}
}

func cognitionDollState() *dollstate.DollState {
	return &dollstate.DollState{
		Identity: dollstate.Identity{DollID: "doll-1", CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Revision: 1, Content: "Curious and kind."},
		Owner:    dollstate.Owner{OwnerID: "owner-1", Name: "Owner"},
		Drives: dollstate.Drives{Items: []dollstate.DriveItem{{
			ID: "drive-1", Name: "Understand",
		}}},
		Goals: dollstate.Goals{Items: []dollstate.GoalItem{{
			ID: "goal-1", Name: "Inspect the signal", State: dollstate.GoalStateActive,
		}}},
		Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
			ID: "intention-1", Subject: "Reconsider the signal", State: dollstate.IntentionStatePending,
		}}},
		Memories: dollstate.Memories{Items: []dollstate.MemoryItem{{
			ID: "memory-1", Kind: dollstate.KindObservation, Content: "A relevant prior observation", Sequence: 0,
		}}},
	}
}

func assertCognitionStateAvailable(t *testing.T, result contextbuilder.Result) {
	t.Helper()
	content := joinContents(result)
	for _, want := range []string{"Spark", "Inspect the signal", "Reconsider the signal", "A relevant prior observation", "spontaneous internal wake", "goal-1"} {
		if !strings.Contains(content, want) {
			t.Errorf("context missing %q: %s", want, content)
		}
	}
}

func joinContents(result contextbuilder.Result) string {
	var contents []string
	for _, message := range result.Messages {
		contents = append(contents, message.Content)
	}
	return strings.Join(contents, "\n")
}

func latestContents(result contextbuilder.Result, count int) []string {
	start := len(result.Messages) - count
	contents := make([]string, count)
	for index, message := range result.Messages[start:] {
		contents[index] = message.Content
	}
	return contents
}

func hasProvenance(messages []contextbuilder.Message, provenance string) bool {
	for _, message := range messages {
		if message.Provenance == provenance {
			return true
		}
	}
	return false
}
