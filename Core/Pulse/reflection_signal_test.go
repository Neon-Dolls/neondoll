package pulse

import (
	"math"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestReflectivePressureIsDistinctDeterministicActivation(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	snapshot := EvaluateSignals(now, config.PulseConfig{Enabled: true}, PulseSnapshot{ReflectivePressure: 0.4}, nil, InhibitionInputs{})
	if snapshot.Reflective != 0.4 || snapshot.Idle != 0 || len(snapshot.Subjects) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	pressure, effective, signals, _ := EvaluatePressure(snapshot)
	if pressure != 0.4 || effective != 0.4 || len(signals) != 1 || signals[0] != 0.4 {
		t.Fatalf("pressure=%v effective=%v signals=%v", pressure, effective, signals)
	}
}

func TestRunnerAccumulatesAndExplicitlySettlesReflectivePressure(t *testing.T) {
	runner := NewRunner(config.PulseConfig{Enabled: true}, NewFakeClock(time.Unix(100, 0)), nil, logger.New(logger.ErrorLevel, nil), nil)
	runner.AddReflectivePressure(0.25)
	runner.AddReflectivePressure(0.5)
	want := 1 - (1-0.25)*(1-0.5)
	if got := runner.Snapshot().ReflectivePressure; math.Abs(got-want) > 1e-9 {
		t.Fatalf("pressure = %v, want %v", got, want)
	}
	runner.SettleReflectivePressure(0.2)
	if got := runner.Snapshot().ReflectivePressure; math.Abs(got-(want-0.2)) > 1e-9 {
		t.Fatalf("settled pressure = %v", got)
	}
}

func TestReflectivePressureSurvivesCheckpointWithoutForcingReflection(t *testing.T) {
	clock := NewFakeClock(time.Unix(100, 0))
	runner := NewRunner(config.PulseConfig{Enabled: true}, clock, nil, logger.New(logger.ErrorLevel, nil), nil)
	runner.AddReflectivePressure(0.6)
	checkpoint := runner.ToCheckpoint()
	restarted := NewRunner(config.PulseConfig{Enabled: true}, clock, nil, logger.New(logger.ErrorLevel, nil), nil)
	restarted.RestoreFromCheckpoint(checkpoint)
	if got := restarted.Snapshot().ReflectivePressure; got != 0.6 {
		t.Fatalf("restored pressure = %v", got)
	}
	// Restoring operational pressure reconstructs an opportunity input only;
	// no Reflection entry point or inference is invoked by restoration.
	if restarted.CognitionRunActive() {
		t.Fatal("checkpoint restoration started cognition")
	}
}
