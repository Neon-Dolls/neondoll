package dollmind

import "github.com/Neon-Dolls/neondoll/DollLink/Events"

// MindPath tells Core what cognition is needed after L0 Reflex has processed
// an event.
//
//	event + relevant Doll State  →  L0 REFLEX  →  MindPath
//
// PathSleep  — L0 handled the event deterministically; no inference needed.
// PathOrient — the event requires L1 orientation (inference required).
type MindPath int

const (
	PathSleep  MindPath = iota // no further cognition — handled by L0
	PathOrient                 // needs L1 orientation — inference required
)

// String returns the canonical name of the mind path.
func (p MindPath) String() string {
	switch p {
	case PathSleep:
		return "sleep"
	case PathOrient:
		return "orient"
	default:
		return "unknown"
	}
}

// GoString satisfies the GoStringer interface for diagnostic output.
func (p MindPath) GoString() string {
	return "dollmind." + p.String()
}

// L0Reflex is the deterministic L0 entry boundary.
//
// It evaluates whether a given event type requires cognition beyond the
// deterministic Reflex layer.  No inference provider is called, no random
// state is consulted, and no wall-clock time affects the result.
//
// Known event types that can be answered without inference return PathSleep.
// Events that need semantic interpretation (messages, arbitrary commands)
// return PathOrient.
//
// The function is purely deterministic: given the same event type, it always
// returns the same MindPath.
func L0Reflex(eventType events.Type) MindPath {
	switch eventType {
	case events.TypeMessage:
		// Messages always need semantic interpretation — route to L1.
		return PathOrient
	case events.TypeCommand:
		// Commands may have known-knowns, but without content inspection
		// they belong to L1.
		return PathOrient
	case events.TypeInternalWake:
		// A self-set intention has become due — Spark must reconsider it.
		return PathOrient
	default:
		// Presence, system, and unknown events are handled deterministically
		// at the Reflex layer without inference.
		return PathSleep
	}
}
