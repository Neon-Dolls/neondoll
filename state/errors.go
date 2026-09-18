package state

import "fmt"

// StateError wraps state-related errors.
type StateError struct {
	Op  string
	Err error
}

func (e *StateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("state %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("state %s", e.Op)
}

func (e *StateError) Unwrap() error { return e.Err }