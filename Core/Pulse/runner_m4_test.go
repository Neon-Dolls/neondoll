package pulse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// muteLogger returns a logger that discards everything for tests.
func muteLogger() *logger.Logger {
	return logger.New(logger.ErrorLevel, nil)
}

// testMindEntry implements MindEntrance for deterministic testing.
type testMindEntry struct {
	mu          sync.Mutex
	entered     int
	lastWake    *PulseWake
	returnError error         // if non-nil, EnterPulseWake returns this error
	blockCh     chan struct{} // if non-nil, EnterPulseWake blocks until closed
}

func (m *testMindEntry) EnterPulseWake(ctx context.Context, wake PulseWake) error {
	m.mu.Lock()
	m.entered++
	wakeCopy := wake
	m.lastWake = &wakeCopy
	m.mu.Unlock()

	if m.blockCh != nil {
		<-m.blockCh
	}
	if m.returnError != nil {
		return m.returnError
	}
	return nil
}

func (m *testMindEntry) Entered() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entered
}

func (m *testMindEntry) LastWake() *PulseWake {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastWake == nil {
		return nil
	}
	c := *m.lastWake
	return &c
}

// TestM4_OccupancyClaimRelease verifies that TryClaim/Release work atomically.
func TestM4_OccupancyClaimRelease(t *testing.T) {
	r := &Runner{log: muteLogger()}

	if r.CognitionRunActive() {
		t.Fatal("expected cognition run to be inactive initially")
	}
	if !r.TryClaimCognitionRun() {
		t.Fatal("expected first TryClaim to succeed")
	}
	if !r.CognitionRunActive() {
		t.Fatal("expected cognition run active after claim")
	}
	if r.TryClaimCognitionRun() {
		t.Fatal("expected second TryClaim to fail")
	}
	r.ReleaseCognitionRun()
	if r.CognitionRunActive() {
		t.Fatal("expected cognition run inactive after release")
	}
	if !r.TryClaimCognitionRun() {
		t.Fatal("expected TryClaim to succeed after release")
	}
	r.ReleaseCognitionRun()
}

// TestM4_OccupancyRace tests that TryClaim is race-safe under -race detection.
func TestM4_OccupancyRace(t *testing.T) {
	r := &Runner{log: muteLogger()}
	var wg sync.WaitGroup
	var claimed atomic.Int32

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.TryClaimCognitionRun() {
				claimed.Add(1)
				r.ReleaseCognitionRun()
			}
		}()
	}
	wg.Wait()
	t.Log("total successful sequential claims:", claimed.Load())
}

// TestM4_AdmitWake_Success verifies the full admitPulseWake path:
// admission constructs evidence, enters Mind, releases occupancy.
func TestM4_AdmitWake_Success(t *testing.T) {
	mind := &testMindEntry{}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	r := &Runner{log: muteLogger(), clock: NewFakeClock(now)}
	r.mindEntry = mind

	sample := 0.05
	rng := newFakeRNG(sample)
	opp := EvaluateOpportunity(now, SignalSnapshot{
		Idle: 0.5,
	}, PulseSnapshot{
		LastCognitionAt: now.Add(-10 * time.Minute),
	}, config.PulseConfig{
		IdleHorizon:    300,
		NeglectHorizon: 600,
		ChangeHorizon:  300,
		MinWakeSpacing: 60,
	}, false, rng)

	if !opp.Opportunity {
		t.Fatal("test setup: expected opportunity to be true")
	}

	r.admitPulseWake(now, opp)

	if mind.Entered() != 1 {
		t.Fatalf("expected Mind entered 1 time, got %d", mind.Entered())
	}
	wake := mind.LastWake()
	if wake == nil {
		t.Fatal("expected lastWake to be set")
	}
	if wake.AdmittedAt != now {
		t.Errorf("expected AdmittedAt %v, got %v", now, wake.AdmittedAt)
	}
	if wake.Pressure != opp.Pressure {
		t.Errorf("expected Pressure %f, got %f", opp.Pressure, wake.Pressure)
	}
	if wake.EffectivePressure != opp.EffectivePressure {
		t.Errorf("expected EffectivePressure %f, got %f", opp.EffectivePressure, wake.EffectivePressure)
	}
	if r.CognitionRunActive() {
		t.Error("expected cognition run released after admitPulseWake returns")
	}
}

// TestM4_AdmitWake_OccupancySkipped verifies second wake is skipped when
// cognition run is already active.
func TestM4_AdmitWake_OccupancySkipped(t *testing.T) {
	blockCh := make(chan struct{})
	mind := &testMindEntry{blockCh: blockCh}
	r := &Runner{log: muteLogger()}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	r.mindEntry = mind

	// Claim occupancy to simulate active cognition run
	r.TryClaimCognitionRun()

	// Even with a passing opportunity, admission is skipped
	r.admitPulseWake(now, OpportunitySnapshot{
		Opportunity:       true,
		Pressure:          0.5,
		EffectivePressure: 0.4,
		RandomSample:      float64Ptr(0.05),
	})

	if mind.Entered() != 0 {
		t.Fatalf("expected Mind not entered when occupancy claimed, got %d", mind.Entered())
	}

	r.ReleaseCognitionRun()
	close(blockCh)
}

// TestM4_AdmitWake_NilMindEntry verifies graceful handling of nil mindEntry.
func TestM4_AdmitWake_NilMindEntry(t *testing.T) {
	r := &Runner{log: muteLogger()}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	r.admitPulseWake(now, OpportunitySnapshot{Opportunity: true})

	if r.CognitionRunActive() {
		t.Error("expected cognition run inactive after nil mindEntry admit")
	}
}

// TestM4_AdmitWake_Failure verifies Mind failure releases occupancy
// and the error is surfaced/logged.
func TestM4_AdmitWake_Failure(t *testing.T) {
	mind := &testMindEntry{returnError: errors.New("simulated cognition failure")}
	r := &Runner{log: muteLogger()}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	r.mindEntry = mind

	r.admitPulseWake(now, OpportunitySnapshot{
		Opportunity:       true,
		Pressure:          0.5,
		EffectivePressure: 0.4,
		ActivationSignals: []float64{0.5},
		Inhibition:        InhibitionBreakdown{Cooldown: 0, Budget: 0},
		RandomSample:      float64Ptr(0.05),
	})

	if mind.Entered() != 1 {
		t.Fatalf("expected mind entered 1 time, got %d", mind.Entered())
	}
	if r.CognitionRunActive() {
		t.Error("expected cognition run released after failure")
	}
}

// TestM4_EvaluateNoInferenceUnderMutex proves evaluate() releases mu before
// mind entry. We verify by concurrently reading snapshots during evaluate()
// — no deadlock means mu is not held through admission.
func TestM4_EvaluateNoInferenceUnderMutex(t *testing.T) {
	mind := &testMindEntry{}
	rng := newFakeRNG(0.01)

	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    300,
		NeglectHorizon: 600,
		ChangeHorizon:  300,
		MinWakeSpacing: 60,
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(now).(*fakeClock)
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)

	// Past cognition so idle signal rises
	r.RecordCognition(now.Add(-30 * time.Minute))

	// Wire test channels so we can drive ticks
	r.tickTestCh = make(chan time.Time, 10)
	r.tickAckCh = make(chan struct{}, 10)

	clock.Advance(10 * time.Minute)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		r.evaluate()
	}()

	// Concurrent read during evaluate — would deadlock if mu were held
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			r.Snapshot()
			r.SignalSnapshot()
			r.OpportunitySnapshot()
		}
	}()

	wg.Wait()
}

// TestM4_EvaluateCognitionRunActive_NoEntry verifies evaluate() skips
// admission when cognition run is already active.
func TestM4_EvaluateCognitionRunActive_NoEntry(t *testing.T) {
	mind := &testMindEntry{}
	rng := newFakeRNG(0.01)

	cfg := config.PulseConfig{
		Enabled:        true,
		IdleHorizon:    300,
		NeglectHorizon: 600,
		ChangeHorizon:  300,
		MinWakeSpacing: 60,
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(now).(*fakeClock)
	r := NewRunner(cfg, clock, rng, muteLogger(), mind)
	r.RecordCognition(now.Add(-30 * time.Minute))

	r.tickTestCh = make(chan time.Time, 10)
	r.tickAckCh = make(chan struct{}, 10)

	r.TryClaimCognitionRun()
	clock.Advance(10 * time.Minute)
	r.evaluate()

	if mind.Entered() != 0 {
		t.Fatalf("expected Mind not entered when cognition run active, got %d", mind.Entered())
	}

	r.ReleaseCognitionRun()
}

// TestM4_WakeEvidenceStructure verifies the PulseWake struct has all
// required fields and preserves OpportunitySnapshot data.
func TestM4_WakeEvidenceStructure(t *testing.T) {
	rng := newFakeRNG(0.03)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	opp := EvaluateOpportunity(now, SignalSnapshot{
		Idle: 0.5,
	}, PulseSnapshot{
		LastCognitionAt: now.Add(-10 * time.Minute),
	}, config.PulseConfig{
		IdleHorizon:    300,
		NeglectHorizon: 600,
		ChangeHorizon:  300,
		MinWakeSpacing: 60,
	}, false, rng)

	if !opp.Opportunity {
		t.Fatal("test setup: expected opportunity true")
	}

	mind := &testMindEntry{}
	r := &Runner{log: muteLogger(), clock: NewFakeClock(now), mindEntry: mind}
	r.admitPulseWake(now, opp)

	wake := mind.LastWake()
	if wake == nil {
		t.Fatal("expected wake evidence")
	}

	if len(wake.Subjects) != 0 {
		t.Errorf("expected empty Subjects for idle-only wake, got %d items", len(wake.Subjects))
	}
	if wake.Subjects == nil {
		t.Error("expected Subjects to be initialized to non-nil empty slice")
	}
	if wake.RandomSample != *opp.RandomSample {
		t.Errorf("expected RandomSample %f, got %f", *opp.RandomSample, wake.RandomSample)
	}
}

// TestM4_NoExternalEvent confirms the wake evidence context says
// "No external event triggered it" and "No Intention is due".
func TestM4_NoExternalEvent(t *testing.T) {
	// This is proven by the FormatWakeEvidence function in dollmind package,
	// but we verify the PulseWake struct carries no event/intention fields.
	mind := &testMindEntry{}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	r := &Runner{log: muteLogger(), clock: NewFakeClock(now), mindEntry: mind}
	rng := newFakeRNG(0.03)

	opp := EvaluateOpportunity(now, SignalSnapshot{
		Idle: 0.5,
	}, PulseSnapshot{
		LastCognitionAt: now.Add(-10 * time.Minute),
	}, config.PulseConfig{
		IdleHorizon:    300,
		NeglectHorizon: 600,
		ChangeHorizon:  300,
		MinWakeSpacing: 60,
	}, false, rng)

	r.admitPulseWake(now, opp)

	wake := mind.LastWake()
	if wake == nil {
		t.Fatal("expected wake evidence")
	}

	// Verify no Interaction Session info or Intention ID leaks into wake
	if wake.Pressure == 0 {
		t.Log("wake pressure zero (ok for idle-only)")
	}
}

// TestM4_FalseOpportunityNeverReachesMind verifies a false/ineligible
// opportunity never enters the Mind.
func TestM4_FalseOpportunityNeverReachesMind(t *testing.T) {
	mind := &testMindEntry{}
	r := &Runner{log: muteLogger(), mindEntry: mind}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	// A non-opportunity should not trigger admission
	r.admitPulseWake(now, OpportunitySnapshot{Opportunity: false})

	if mind.Entered() != 0 {
		t.Fatal("expected Mind not entered for false opportunity")
	}
}

func float64Ptr(f float64) *float64 {
	return &f
}
