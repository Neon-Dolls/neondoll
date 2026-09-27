package dollmind

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// FormatWakeEvidence tests
// ──────────────────────────────────────────────

func TestFormatWakeEvidence_IdleOnly(t *testing.T) {
	wake := pulse.PulseWake{
		AdmittedAt:        time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Pressure:          0.45,
		EffectivePressure: 0.30,
		ActivationSignals: []float64{0.45},
		Inhibition:        pulse.InhibitionBreakdown{Cooldown: 0.0, Budget: 0.0},
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.03,
	}

	text := FormatWakeEvidence(wake)

	// Must contain canonical no-external-event language
	wants := []string{
		"initiated by your own continuing internal runtime state",
		"No external event triggered it",
		"No Intention is due",
		"0.4500",
		"0.3000",
		"Subjects: (none — idle-only wake)",
		"0.0300",
	}
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("expected FormatWakeEvidence to contain %q", w)
		}
	}

	// Must NOT mention an external event, Intention ID, or Interaction Session
	notWants := []string{
		"IntentionID",
		"Interaction Session",
		"external event",
		"user message",
		"due intention",
	}
	for _, n := range notWants {
		if strings.Contains(text, n) {
			// "No external event" and "No Intention" are allowed; any other mention is not
			if !strings.Contains(text, "No "+n) && !strings.Contains(text, "No "+strings.Title(n)) {
				t.Errorf("expected FormatWakeEvidence to NOT contain %q", n)
			}
		}
	}
}

func TestFormatWakeEvidence_WithSubjects(t *testing.T) {
	wake := pulse.PulseWake{
		AdmittedAt:        time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Pressure:          0.72,
		EffectivePressure: 0.65,
		ActivationSignals: []float64{0.50, 0.22},
		Inhibition:        pulse.InhibitionBreakdown{Cooldown: 0.10, Budget: 0.05},
		Subjects: []pulse.SubjectActivation{
			{SubjectID: "subj_1", Neglect: 0.4, Change: 0.0, Unfinished: 0.0},
			{SubjectID: "subj_2", Neglect: 0.0, Change: 0.3, Unfinished: 0.1},
		},
		RandomSample: 0.02,
	}

	text := FormatWakeEvidence(wake)

	wants := []string{
		"subj_1",
		"subj_2",
		"neglect=0.4000",
		"change=0.3000",
		"unfinished=0.1000",
		"0.1000/0.0500", // inhibition cooldown/budget
		"0.0200",
	}
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("expected FormatWakeEvidence to contain %q", w)
		}
	}
}

func TestFormatWakeEvidence_NoInhibition(t *testing.T) {
	wake := pulse.PulseWake{
		Pressure:          0.3,
		EffectivePressure: 0.3,
		Inhibition:        pulse.InhibitionBreakdown{Cooldown: 0, Budget: 0},
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.1,
	}
	text := FormatWakeEvidence(wake)
	// No inhibition line should appear when both are zero
	if strings.Contains(text, "Inhibition") {
		t.Error("expected no Inhibition line when both cooldown and budget are zero")
	}
}

// ──────────────────────────────────────────────
// L0Reflex routing tests
// ──────────────────────────────────────────────

func TestL0Reflex_PulseSpontaneous(t *testing.T) {
	if path := L0Reflex(events.TypePulseSpontaneous); path != PathOrient {
		t.Errorf("expected PathOrient for TypePulseSpontaneous, got %v", path)
	}
}

func TestL0Reflex_OtherTypesUnchanged(t *testing.T) {
	// Existing behaviors must not change
	if path := L0Reflex(events.TypeInternalWake); path != PathOrient {
		t.Errorf("expected PathOrient for TypeInternalWake, got %v", path)
	}
	if path := L0Reflex(events.TypePresence); path != PathSleep {
		t.Errorf("expected PathSleep for TypePresence, got %v", path)
	}
	if path := L0Reflex(events.TypeUnknown); path != PathSleep {
		t.Errorf("expected PathSleep for TypeUnknown, got %v", path)
	}
}

// ──────────────────────────────────────────────
// buildOrientPrompt Pulse context tests
// ──────────────────────────────────────────────

func TestBuildOrientPrompt_PulseSpontaneous(t *testing.T) {
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	input := "This cognition was initiated by your own continuing internal runtime state.\nNo external event triggered it.\nNo Intention is due."
	prompt := buildOrientPrompt(state, events.TypePulseSpontaneous, input)

	// Must contain the identity and the raw input
	if !strings.Contains(prompt, "Spark") {
		t.Error("expected prompt to contain identity name")
	}
	if !strings.Contains(prompt, "initiated by your own continuing") {
		t.Error("expected prompt to contain the wake evidence input")
	}
	// Must NOT say "An intention from within your own mind has become due"
	if strings.Contains(prompt, "An intention from within your own mind") {
		t.Error("PulseSpontaneous must NOT use TypeInternalWake language")
	}
}

func TestBuildOrientPrompt_PulseSpontaneousVsInternalWake(t *testing.T) {
	// Verify that TypePulseSpontaneous and TypeInternalWake produce different event lines
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}

	pulsePrompt := buildOrientPrompt(state, events.TypePulseSpontaneous, "pulse wake data")
	wakePrompt := buildOrientPrompt(state, events.TypeInternalWake, "intention data")

	if strings.Contains(pulsePrompt, "An intention from within your own mind") {
		t.Error("PulseSpontaneous must not say 'intention from within your own mind'")
	}
	if !strings.Contains(wakePrompt, "An intention from within your own mind") {
		t.Error("InternalWake must say 'intention from within your own mind'")
	}
}

// ──────────────────────────────────────────────
// EnterPulseWake integration tests
// ──────────────────────────────────────────────

func TestEnterPulseWake_MattersFalse(t *testing.T) {
	// matters=false: L1 Orient returns "does not matter", no L2, returns LevelOrient
	provider := &orientTestProvider{
		name:     "test",
		response: `{"summary":"Internal state is calm, nothing requires attention.","matters":false,"reason":"Idle-only wake with no pressing goals or changes."}`,
	}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		AdmittedAt:        time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Pressure:          0.45,
		EffectivePressure: 0.30,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.03,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned unexpected error: %v", err)
	}
}

// TestEnterPulseWake_MattersFalse_NoL2 proves that a matters=false pulse wake
// does NOT proceed to L2 Plan. We verify by checking the provider was called
// exactly once (for L1 Orient, not twice for Orient+Plan).
func TestEnterPulseWake_MattersFalse_NoL2(t *testing.T) {
	provider := &orientTestProvider{
		name:     "test",
		response: `{"summary":"Nothing matters right now.","matters":false,"reason":"Idle-only wake, no activity."}`,
	}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		Pressure:          0.3,
		EffectivePressure: 0.2,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.05,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned unexpected error: %v", err)
	}

	// L1 Orient called exactly once, no L2 Plan call
	if n := provider.callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inference call (L1 only), got %d", n)
	}
}

func TestEnterPulseWake_MattersTrue_EscalatesToL2(t *testing.T) {
	// matters=true: L1 says it matters, then L2 Plan is called.
	// We use a custom provider that returns different responses on first vs second call.
	p := &mattersTrueProvider{
		orientResponse: `{"summary":"I feel an impulse to check in.","matters":true,"reason":"Internal state suggests re-engagement."}`,
		planResponse:   `{"summary":"Spark decides to observe silently.","proposed_action":"Observe current state.","observations":["No active events."],"should_reorient":false,"request_future_cognition":false}`,
	}

	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(p, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		Pressure:          0.7,
		EffectivePressure: 0.6,
		Subjects: []pulse.SubjectActivation{
			{SubjectID: "subj_1", Neglect: 0.5},
		},
		RandomSample: 0.01,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned unexpected error for matters=true: %v", err)
	}

	// Provider should have been called twice (L1 + L2)
	if n := p.callCount.Load(); n != 2 {
		t.Fatalf("expected 2 inference calls (L1+L2), got %d", n)
	}
	// Verify the first call was for orientation (check Purpose)
	if p.firstPurpose != inference.PurposeOrient {
		t.Errorf("expected first call purpose Orient, got %v", p.firstPurpose)
	}
	// Verify the second call was for planning
	if p.secondPurpose != inference.PurposePlan {
		t.Errorf("expected second call purpose Plan, got %v", p.secondPurpose)
	}
}

// mattersTrueProvider returns different responses for L1 Orient and L2 Plan.
type mattersTrueProvider struct {
	callCount      atomic.Int64
	firstPurpose   inference.Purpose
	secondPurpose  inference.Purpose
	orientResponse string
	planResponse   string
}

func (p *mattersTrueProvider) Name() string             { return "test" }
func (p *mattersTrueProvider) ID() inference.ProviderID { return "test" }
func (p *mattersTrueProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	p.callCount.Add(1)
	n := p.callCount.Load()
	switch n {
	case 1:
		p.firstPurpose = req.Purpose
		return &inference.Response{
			Content:    p.orientResponse,
			ProviderID: "test",
			TokensUsed: 10,
		}, nil
	case 2:
		p.secondPurpose = req.Purpose
		return &inference.Response{
			Content:    p.planResponse,
			ProviderID: "test",
			TokensUsed: 20,
		}, nil
	default:
		return &inference.Response{
			Content:    `{"summary":"extra","proposed_action":"","observations":[],"should_reorient":false}`,
			ProviderID: "test",
			TokensUsed: 5,
		}, nil
	}
}

func TestEnterPulseWake_Failure(t *testing.T) {
	// L1 Orient fails — error returned, no synthetic Intention created
	provider := &orientTestProvider{
		name:     "test",
		response: "invalid json",
	}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		Pressure:          0.5,
		EffectivePressure: 0.4,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.05,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err == nil {
		t.Fatal("expected error from invalid orientation JSON, got nil")
	}
	if !strings.Contains(err.Error(), "enter pulse wake") {
		t.Errorf("expected error to mention 'enter pulse wake', got: %v", err)
	}

	// No synthetic Intention should be created — the mock state has no Intentions.Items
	if len(state.Intentions.Items) > 0 {
		t.Error("expected no synthetic Intention to be created after failure")
	}
}

// TestEnterPulseWake_NoSyntheticIntention explicitly verifies EnterPulseWake
// does NOT mark any Intention as completed, does NOT create a new Intention,
// and does NOT call Save on MindAPI.
func TestEnterPulseWake_NoSyntheticIntention(t *testing.T) {
	provider := &orientTestProvider{
		name:     "test",
		response: `{"summary":"Internal state is calm.","matters":false,"reason":"No action needed."}`,
	}

	saveCalled := false
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{
				{ID: "existing_int_1", Subject: "test", State: dollstate.IntentionStatePending},
			},
		},
	}

	mockAPI := &saveTrackingMockAPI{
		state: state,
		saveFunc: func() error {
			saveCalled = true
			return nil
		},
	}

	s := New(provider, logger.New(logger.ErrorLevel, nil), mockAPI)

	wake := pulse.PulseWake{
		Pressure:          0.3,
		EffectivePressure: 0.2,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.05,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned unexpected error: %v", err)
	}

	// Save should NOT be called — EnterPulseWake does not persist
	if saveCalled {
		t.Error("Save should not be called by EnterPulseWake")
	}

	// Existing Intention should NOT have been touched
	found := false
	for _, item := range state.Intentions.Items {
		if item.ID == "existing_int_1" {
			found = true
			if item.State != dollstate.IntentionStatePending {
				t.Errorf("existing Intention state changed from Pending to %s — EnterPulseWake must not modify Intentions", item.State)
			}
		}
	}
	if !found {
		t.Error("existing Intention missing from state")
	}
}

// saveTrackingMockAPI tracks whether Save is called.
type saveTrackingMockAPI struct {
	state    *dollstate.DollState
	saveFunc func() error
}

func (m *saveTrackingMockAPI) Inference() inference.Provider { return nil }
func (m *saveTrackingMockAPI) State() *dollstate.DollState   { return m.state }
func (m *saveTrackingMockAPI) Save() error                   { return m.saveFunc() }

// TestEnterPulseWake_EventTypeIsPulseSpontaneous verifies the event type
// passed to Orient is TypePulseSpontaneous, not TypeInternalWake.
// We check indirectly by verifying the prompt uses Pulse-specific language
// (not "An intention from within your own mind").
func TestEnterPulseWake_EventTypeIsPulseSpontaneous(t *testing.T) {
	var lastReq *inference.Request
	provider := &capturingProvider{
		response: `{"summary":"ok","matters":false,"reason":"test"}`,
		capture: func(req *inference.Request) {
			lastReq = req
		},
	}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		Pressure:          0.3,
		EffectivePressure: 0.2,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.05,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned error: %v", err)
	}

	if lastReq == nil {
		t.Fatal("provider was not called")
	}
	if len(lastReq.Messages) == 0 {
		t.Fatal("no messages in provider request")
	}
	systemContent := lastReq.Messages[0].Content

	// Must use Pulse-specific language, NOT TypeInternalWake language
	if strings.Contains(systemContent, "An intention from within your own mind") {
		t.Error("PulseSpontaneous must NOT use 'An intention from within your own mind' language")
	}
	// Must contain the Pulse wake evidence
	if !strings.Contains(systemContent, "initiated by your own continuing internal runtime state") {
		t.Error("expected prompt to contain Pulse wake evidence")
	}
}

// TestEnterPulseWake_RaceFree proves the Scheduler itself does not introduce
// races when handling PulseWake. Each goroutine uses its own provider to
// avoid shared mutable state in the test harness itself.
func TestEnterPulseWake_RaceFree(t *testing.T) {
	const N = 10
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			provider := &orientTestProvider{
				name:     "test",
				response: `{"summary":"ok","matters":false,"reason":"race test"}`,
			}
			state := &dollstate.DollState{
				Identity: dollstate.Identity{CanonicalName: "Spark"},
			}
			s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

			wake := pulse.PulseWake{
				Pressure:          0.3,
				EffectivePressure: 0.2,
				Subjects:          []pulse.SubjectActivation{},
				RandomSample:      0.05,
			}
			errs <- s.EnterPulseWake(context.Background(), wake)
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("race test call returned error: %v", err)
		}
	}
}

// TestEnterPulseWake_EvidenceInOrientPrompt verifies that the FormattedWakeEvidence
// text reaches the Orient prompt.
func TestEnterPulseWake_EvidenceInOrientPrompt(t *testing.T) {
	var lastReq *inference.Request
	provider := &capturingProvider{
		response: `{"summary":"ok","matters":false,"reason":"test"}`,
		capture: func(req *inference.Request) {
			lastReq = req
		},
	}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	s := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	wake := pulse.PulseWake{
		Pressure:          0.45,
		EffectivePressure: 0.30,
		Subjects:          []pulse.SubjectActivation{},
		RandomSample:      0.03,
	}

	err := s.EnterPulseWake(context.Background(), wake)
	if err != nil {
		t.Fatalf("EnterPulseWake returned error: %v", err)
	}

	if lastReq == nil {
		t.Fatal("provider was not called")
	}

	// The last request should contain the wake evidence in the system prompt
	if len(lastReq.Messages) == 0 {
		t.Fatal("no messages in provider request")
	}
	systemContent := lastReq.Messages[0].Content
	wants := []string{
		"initiated by your own continuing internal runtime state",
		"No external event triggered it",
		"No Intention is due",
		"0.4500",
	}
	for _, w := range wants {
		if !strings.Contains(systemContent, w) {
			t.Errorf("expected prompt to contain %q", w)
		}
	}
}

// capturingProvider returns a fixed response and calls capture with the request.
type capturingProvider struct {
	name     string
	response string
	capture  func(*inference.Request)
}

func (p *capturingProvider) Name() string             { return p.name }
func (p *capturingProvider) ID() inference.ProviderID { return inference.ProviderID(p.name) }
func (p *capturingProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	p.capture(&req)
	return &inference.Response{
		Content:    p.response,
		ProviderID: inference.ProviderID(p.name),
		TokensUsed: 10,
	}, nil
}
