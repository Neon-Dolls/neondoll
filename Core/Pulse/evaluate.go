package pulse

import (
	"fmt"
	"time"
)

// Evaluate performs a single synchronous Pulse evaluation using the given clock.
// It is a pure function with no side effects: it does not mutate Doll State,
// call inference providers, call Doll Mind, or create wake events.
//
// In M1, Evaluate always evaluates a single PulseSubject("temporal") and records
// a TemporalObservation for each subject.
//
// Backwards time is detected when the clock returns a time before prev.Now().
// When detected, BackwardsTime is set to true in the result, and a warning-worthy
// observation is recorded.
func Evaluate(clock Clock, prev PulseResult) PulseResult {
	now := clock.Now()
	backwards := !prev.At.IsZero() && now.Before(prev.At)

	subjects := []PulseSubject{"temporal"}
	observations := []TemporalObservation{
		{
			At:        now,
			Subject:   "temporal",
			TickIndex: prev.TickIndex + 1,
		},
	}

	result := PulseResult{
		TickIndex:     prev.TickIndex + 1,
		At:            now,
		Subjects:      subjects,
		Observations:  observations,
		BackwardsTime: backwards,
	}

	if backwards {
		result.At = now
		result.Observations = append(result.Observations, TemporalObservation{
			At:        now,
			Subject:   PulseSubject(fmt.Sprintf("backwards-warning: prev=%s now=%s", prev.At.Format(time.RFC3339), now.Format(time.RFC3339))),
			TickIndex: prev.TickIndex + 1,
		})
	}

	return result
}
