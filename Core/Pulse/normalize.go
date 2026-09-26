package pulse

import "math"

// normalize maps a value relative to a horizon into the range [0, 1].
//
// Semantics (per Core 3 M2 contract):
//   - horizon <= 0 → 0 (disabled)
//   - value   <= 0 → 0
//   - value  >= horizon → 1 (saturated)
//   - otherwise → value / horizon (linear)
//
// Guards against NaN and ±Inf by returning 0.
func normalize(value, horizon float64) float64 {
	if math.IsNaN(value) || math.IsNaN(horizon) {
		return 0
	}
	if math.IsInf(value, 0) || math.IsInf(horizon, 0) {
		return 0
	}
	if horizon <= 0 || value <= 0 {
		return 0
	}
	if value >= horizon {
		return 1
	}
	return value / horizon
}
