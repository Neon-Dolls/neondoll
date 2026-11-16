package dollmind

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// ReflectionSource identifies the cognitive boundary that deliberately
// created a Reflection signal. It is provenance, not authority.
type ReflectionSource string

const (
	ReflectionSourceDoll ReflectionSource = "doll"
	ReflectionSourcePlan ReflectionSource = "plan"
)

var (
	ErrInvalidReflection    = errors.New("dollmind: invalid reflection signal")
	ErrReflectionInProgress = errors.New("dollmind: reflection signal already in progress")
)

// ReflectionSignal is an ephemeral, Core-owned opportunity to reconsider
// something. Durable future reconsideration remains an Intention; Cognitive
// Runs and processed Reflection signals are intentionally not persisted.
type ReflectionSignal struct {
	ID          string
	Source      ReflectionSource
	Reason      string
	Context     string
	InitiatedAt time.Time
	IntentionID string
}

// ReflectionOutcome is the bounded cognition result. Content is deliberately
// open-ended and is not automatically persisted as memory or Self state.
type ReflectionOutcome struct {
	Signal  ReflectionSignal
	Content string
}

// NewReflectionSignal creates a stable signal identity at the actual moment
// Core recognizes the opportunity. Callers may preserve that value across a
// retry, but signals are not durable scheduling records.
func NewReflectionSignal(source ReflectionSource, reason, context string, at time.Time) (ReflectionSignal, error) {
	signal := ReflectionSignal{
		ID:          uuid.NewString(),
		Source:      source,
		Reason:      strings.TrimSpace(reason),
		Context:     strings.TrimSpace(context),
		InitiatedAt: at.UTC(),
	}
	if err := signal.Validate(); err != nil {
		return ReflectionSignal{}, err
	}
	return signal, nil
}

func (signal ReflectionSignal) Validate() error {
	if strings.TrimSpace(signal.ID) == "" || strings.TrimSpace(signal.Reason) == "" || signal.InitiatedAt.IsZero() {
		return ErrInvalidReflection
	}
	switch signal.Source {
	case ReflectionSourceDoll, ReflectionSourcePlan:
		return nil
	default:
		return ErrInvalidReflection
	}
}

// Reflect executes one distinct Reflection signal through the shared,
// tool-enabled CognitiveExecution engine. The signal ID prevents concurrent
// duplicate processing without turning Reflection into a persistent job queue.
func (s *Scheduler) Reflect(ctx context.Context, signal ReflectionSignal) (*ReflectionOutcome, error) {
	if err := signal.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reflectionMu.Lock()
	if _, exists := s.activeReflections[signal.ID]; exists {
		s.reflectionMu.Unlock()
		return nil, ErrReflectionInProgress
	}
	s.activeReflections[signal.ID] = struct{}{}
	s.reflectionMu.Unlock()
	defer func() {
		s.reflectionMu.Lock()
		delete(s.activeReflections, signal.ID)
		s.reflectionMu.Unlock()
	}()

	evidence := formatReflectionEvidence(signal)
	ctx, run, err := s.ensureCognitionRun(ctx, "Reflect on a meaningful experience or change in understanding.", "")
	if err != nil {
		return nil, fmt.Errorf("reflection cognition run: %w", err)
	}
	if signal.IntentionID != "" && run.intention() == "" {
		run.setIntentionID(signal.IntentionID)
	}
	if err := run.appendRecord(contextbuilder.CognitionWorkingRecord{
		Role:       contextbuilder.RoleUser,
		Content:    evidence,
		Provenance: "cognition_run.reflection_signal",
		Required:   true,
	}); err != nil {
		return nil, fmt.Errorf("reflection signal context: %w", err)
	}
	selected, _, err := s.modelForTask(config.InferenceTaskReflection)
	if err != nil {
		return nil, fmt.Errorf("reflection model context budget: %w", err)
	}
	response, err := s.executeCognition(ctx, run, selected, inference.PurposeReflect, 0.3, reflectionInstruction)
	if err != nil {
		return nil, fmt.Errorf("reflection cognitive execution: %w", err)
	}
	return &ReflectionOutcome{Signal: signal, Content: response.Content}, nil
}

func formatReflectionEvidence(signal ReflectionSignal) string {
	text := "Reflection signal " + signal.ID + "\n" +
		"Initiated by: " + string(signal.Source) + "\n" +
		"Recognized at: " + signal.InitiatedAt.Format(time.RFC3339Nano) + "\n" +
		"Why reflection may be worthwhile: " + signal.Reason
	if signal.Context != "" {
		text += "\nRelevant context: " + signal.Context
	}
	if signal.IntentionID != "" {
		text += "\nOriginating intention: " + signal.IntentionID
	}
	return text
}

const reflectionInstruction = `This is REFLECTION: an open opportunity to reconsider what happened, why it matters, whether your prior understanding still fits, and whether anything is worth learning, remembering, investigating, or approaching differently. You may use available Recall or Remember tools selectively. Memory is contextual evidence, not authority. You may conclude that nothing should change, and you need not take an action or retain a memory. Do not initiate another Reflection from this Reflection. Return your considered reflection in natural language; it is not automatically persisted as memory or Self.`
