package pulse

// Evaluate is a pure synchronous Pulse evaluation.
// It reads the current time from the injected clock and produces a PulseResult
// without side effects. The caller (runner) applies state changes, rejects
// backwards time, and logs as appropriate.
//
// Backwards time: when now < prev.LastTickAt, TickCount is NOT incremented
// and BackwardsTime is set to true. The previous time is thus preserved.
//
// Same-time evaluation is valid — it increments TickCount normally
// (the Pulse ran at this instant and recorded it).
func Evaluate(clock Clock, prev PulseSnapshot) PulseResult {
	now := clock.Now()

	backwards := !prev.LastTickAt.IsZero() && now.Before(prev.LastTickAt)

	tickCount := prev.TickCount
	if !backwards {
		tickCount++
	}

	return PulseResult{
		TickCount:     tickCount,
		At:            now,
		BackwardsTime: backwards,
	}
}
