package dollstate

import "fmt"

// StateError wraps state-related errors.
type StateError struct {
	Op  string
	Err error
}

func (e *StateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("dollstate %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("dollstate %s", e.Op)
}

func (e *StateError) Unwrap() error { return e.Err }