package dollmind

import (
	"context"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Level maps to the Doll Mind cognition hierarchy.
type Level int

const (
	LevelReflex  Level = 0
	LevelOrient  Level = 1
	LevelPlan    Level = 2
	LevelReflect Level = 3
	LevelDream   Level = 4
)

// (DefaultRetryDuration removed per M9 P3 review — Core must not invent
// automatic future wake times when cognition completes or fails.)

func (l Level) String() string {
	switch l {
	case LevelReflex:
		return "reflex"
	case LevelOrient:
		return "orient"
	case LevelPlan:
		return "plan"
	case LevelReflect:
		return "reflect"
	case LevelDream:
		return "dream"
	default:
		return "unknown"
	}
}

// MindAPI is the interface between cognition and the inference provider.
type MindAPI interface {
	Inference() inference.Provider
	State() *dollstate.DollState
	// Save durably persists the current Doll State through the Core
	// persistence boundary. Called by Plan after materialising Intention.
	Save() error
}

// Result from a cognition cycle.
type Result struct {
	Level       Level
	Actions     []Action
	StateDirty  bool
	Orientation *Orientation // set when Level is LevelOrient
	Plan        *Plan        // set when Level is LevelPlan
}

// Action represents something the Doll should do.
type Action struct {
	Type    string
	Payload map[string]any
}

// ActionKind identifies the kind of outbound action Spark may propose.
// Each kind maps to a deterministic Core-mapped action in Doll Link.
type ActionKind string

const (
	// ActionKindSendText means Spark wants to send text content to
	// a connected Body / client through Doll Link.
	ActionKindSendText ActionKind = "send_text"
)

// OutboundAction is a purely semantic proposal of an action Spark wants
// to take. It does NOT contain transport details, connection IDs,
// WebSocket frames, or implementation specifics — those are Core's
// responsibility to resolve in Phase 2.
type OutboundAction struct {
	Kind    ActionKind `json:"kind"`
	Content string     `json:"content"`
}

// Dispatcher dispatches semantic OutboundAction proposals from Plan to
// their Doll Link transport. The Mind has no direct transport access —
// all outbound actions go through this interface.
type Dispatcher interface {
	Dispatch(ctx context.Context, action OutboundAction) error
}

// Option configures the Scheduler.
type Option func(*Scheduler)

// WithDispatcher sets the outbound action dispatcher.
func WithDispatcher(d Dispatcher) Option {
	return func(s *Scheduler) { s.dispatcher = d }
}

// Scheduler manages cognition cycles.
type Scheduler struct {
	provider     inference.Provider
	log          *logger.Logger
	mindAPI      MindAPI
	dispatcher   Dispatcher
	timeProvider func() time.Time
	toolExecutor *ToolExecutor
}

func New(provider inference.Provider, log *logger.Logger, mindAPI MindAPI, opts ...Option) *Scheduler {
	s := &Scheduler{
		provider:     provider,
		log:          log,
		mindAPI:      mindAPI,
		timeProvider: time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ToolCallLimit is the maximum number of tool calls allowed in a single
// Cognition Run (Core 2 default). This counts actual tool calls, not
// provider round-trips.
const ToolCallLimit = 10

// WithTimeProvider sets the reference time source for DueIntentions and
// Intention validation. Production defaults to time.Now; tests use a
// fixed reference time for deterministic behaviour.
func WithTimeProvider(tp func() time.Time) Option {
	return func(s *Scheduler) { s.timeProvider = tp }
}

// WithToolExecutor sets the tool executor for tool-enabled cognition.
// When set, Plan() uses the provider-native tool loop: tools are projected
// from the local body, tool calls are executed via Guard.Execute, and results
// are fed back to the provider until final content is produced.
func WithToolExecutor(executor *ToolExecutor) Option {
	return func(s *Scheduler) { s.toolExecutor = executor }
}

// Enter is the cognition entry boundary.
//
// It runs L0 Reflex to determine whether inference is needed:
//
//   - PathSleep  → LevelReflex result (deterministic, handled at L0).
//   - PathOrient → L1 Orient; if the event matters, L2 Plan produces a
//     structured plan. Returns LevelPlan when both L1 and L2
//     complete, LevelOrient when L1 decides the event does not
//     warrant planning.
//
// Enter may mutate Doll State through L2 Plan when the plan requests
// future cognition, materialising a pending Intention into state.
func (s *Scheduler) Enter(ctx context.Context, eventType events.Type, input string) (*Result, error) {
	path := L0Reflex(eventType)

	switch path {
	case PathSleep:
		s.log.Info("cognition sleep — L0 handled deterministically",
			map[string]any{"event_type": string(eventType)})
		return &Result{Level: LevelReflex}, nil
	case PathOrient:
		s.log.Info("cognition orient — L1 orientation requested",
			map[string]any{"event_type": string(eventType)})
		orient, err := s.Orient(ctx, eventType, input)
		if err != nil {
			return nil, fmt.Errorf("enter orient: %w", err)
		}
		if !orient.Matters {
			return &Result{Level: LevelOrient, Orientation: orient}, nil
		}
		// Event matters — proceed to L2 planning
		s.log.Info("cognition plan — L2 planning requested",
			map[string]any{"event_type": string(eventType), "orientation": orient.Summary})
		plan, dirty, err := s.Plan(ctx, eventType, input, orient)
		if err != nil {
			return nil, fmt.Errorf("enter plan: %w", err)
		}
		result := &Result{Level: LevelPlan, Orientation: orient, Plan: plan, StateDirty: dirty}

		// Dispatch outbound action if present.
		// A dispatcher failure must be surfaced through the normal error
		// boundary — not buried in a nil-error result.
		if plan.OutboundAction != nil && s.dispatcher != nil {
			if err := s.dispatcher.Dispatch(ctx, *plan.OutboundAction); err != nil {
				s.log.Warn("outbound action dispatch failed", map[string]any{"error": err.Error()})
				return nil, fmt.Errorf("enter dispatch: %w", err)
			}
		}

		return result, nil
	default:
		// Defensive: unknown mind paths fall back to sleep.
		return &Result{Level: LevelReflex}, nil
	}
}

// EnterWake is the cognition entry boundary for internal wake events.
//
// The lifecycle is minimal:
//
//	pending → wake cognition → completed → Save
//
// A failed cognition leaves the Intention pending with the error surfaced.
// A successfully completed wake cognition is fulfillment of the Intention
// regardless of whether L1 concluded matters=false — Spark woke and
// reconsidered what she intended to reconsider; the obligation was fulfilled.
// matters=false means no L2 is needed, not "retry in one hour."
//
// Steps:
//  1. Find the Intention in Doll State (must be Pending)
//  2. Build inference-facing text from payload fields
//  3. Enter cognition (L0 → L1 → optional L2)
//  4. If cognition succeeds → mark Completed
//  5. If cognition fails → leave Pending, surface the error
//  6. Persist via MindAPI.Save()
func (s *Scheduler) EnterWake(ctx context.Context, payload events.IntentionWakePayload) (*Result, error) {
	if s.mindAPI == nil {
		return nil, fmt.Errorf("enter wake: mind API not set")
	}

	// 1. Find the Intention — must be Pending.
	state := s.mindAPI.State()
	found := false
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == payload.IntentionID {
			if state.Intentions.Items[i].State != dollstate.IntentionStatePending {
				return nil, fmt.Errorf("enter wake: intention %q is in state %q, expected %q",
					payload.IntentionID, state.Intentions.Items[i].State, dollstate.IntentionStatePending)
			}
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("enter wake: intention %q not found in state", payload.IntentionID)
	}

	// 2. Build inference-facing text from payload fields.
	text := payload.Subject
	if payload.Description != "" {
		text += "\n" + payload.Description
	}
	if payload.IntentionID != "" {
		text += "\nIntentionID: " + payload.IntentionID
	}

	// 3. Enter cognition (L0 → L1 → optional L2).
	result, err := s.Enter(ctx, events.TypeInternalWake, text)
	if err != nil {
		// Cognition failed — leave Intention Pending, surface the error.
		// The caller can decide whether to retry. Core does not invent
		// an automatic new wake time.
		return nil, fmt.Errorf("enter wake: cognition failed: %w", err)
	}

	// 4. Cognition succeeded — mark Completed (obligation fulfilled).
	//    matters=false still counts: Spark woke and reconsidered.
	s.completeIntention(payload.IntentionID)

	// 5. Persist final state.
	if err := s.mindAPI.Save(); err != nil {
		return nil, fmt.Errorf("enter wake: persist completed state: %w", err)
	}

	return result, nil
}

// completeIntention marks an intention as Completed — it will not be
// rediscovered as due by any future recognition loop.
func (s *Scheduler) completeIntention(id string) {
	state := s.mindAPI.State()
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == id {
			state.Intentions.Items[i].State = dollstate.IntentionStateCompleted
			return
		}
	}
}

// Run executes a cognition cycle at the given level, always calling the provider.
func (s *Scheduler) Run(ctx context.Context, level Level, input string) (*Result, error) {
	s.log.Info("cognition run starting",
		map[string]any{"level": level.String(), "input_len": len(input)})

	resp, err := s.provider.Infer(ctx, inference.Request{
		Messages: []inference.Message{{Role: "user", Content: input}},
		Purpose:  inference.PurposeRespond,
	})
	if err != nil {
		return nil, err
	}

	result := &Result{
		Level: level,
		Actions: []Action{{
			Type:    "respond",
			Payload: map[string]any{"text": resp.Content},
		}},
	}

	s.log.Info("cognition run complete",
		map[string]any{"level": level.String(), "tokens": resp.TokensUsed})
	return result, nil
}
