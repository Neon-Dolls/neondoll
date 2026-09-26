package pulse

import (
	"math"
	"testing"
)

func TestNormalize_ZeroHorizon(t *testing.T) {
	if got := normalize(100, 0); got != 0 {
		t.Errorf("normalize(100, 0) = %f, want 0", got)
	}
}

func TestNormalize_NegativeHorizon(t *testing.T) {
	if got := normalize(100, -1); got != 0 {
		t.Errorf("normalize(100, -1) = %f, want 0", got)
	}
}

func TestNormalize_ZeroValue(t *testing.T) {
	if got := normalize(0, 100); got != 0 {
		t.Errorf("normalize(0, 100) = %f, want 0", got)
	}
}

func TestNormalize_NegativeValue(t *testing.T) {
	if got := normalize(-50, 100); got != 0 {
		t.Errorf("normalize(-50, 100) = %f, want 0", got)
	}
}

func TestNormalize_ExactHorizon(t *testing.T) {
	if got := normalize(100, 100); got != 1.0 {
		t.Errorf("normalize(100, 100) = %f, want 1", got)
	}
}

func TestNormalize_OverHorizon(t *testing.T) {
	if got := normalize(200, 100); got != 1.0 {
		t.Errorf("normalize(200, 100) = %f, want 1", got)
	}
}

func TestNormalize_Linear(t *testing.T) {
	if got := normalize(5, 10); got != 0.5 {
		t.Errorf("normalize(5, 10) = %f, want 0.5", got)
	}
}

func TestNormalize_LinearPrecise(t *testing.T) {
	if got := normalize(1, 4); got != 0.25 {
		t.Errorf("normalize(1, 4) = %f, want 0.25", got)
	}
}

func TestNormalize_NanValue(t *testing.T) {
	if got := normalize(math.NaN(), 100); got != 0 {
		t.Errorf("normalize(NaN, 100) = %f, want 0", got)
	}
}

func TestNormalize_NanHorizon(t *testing.T) {
	if got := normalize(50, math.NaN()); got != 0 {
		t.Errorf("normalize(50, NaN) = %f, want 0", got)
	}
}

func TestNormalize_InfValue(t *testing.T) {
	if got := normalize(math.Inf(1), 100); got != 0 {
		t.Errorf("normalize(+Inf, 100) = %f, want 0", got)
	}
}

func TestNormalize_NegInfValue(t *testing.T) {
	if got := normalize(math.Inf(-1), 100); got != 0 {
		t.Errorf("normalize(-Inf, 100) = %f, want 0", got)
	}
}

func TestNormalize_InfHorizon(t *testing.T) {
	if got := normalize(50, math.Inf(1)); got != 0 {
		t.Errorf("normalize(50, +Inf) = %f, want 0", got)
	}
}

func TestNormalize_NegInfHorizon(t *testing.T) {
	if got := normalize(50, math.Inf(-1)); got != 0 {
		t.Errorf("normalize(50, -Inf) = %f, want 0", got)
	}
}
