package pulse

import (
	"sync"
	"time"
)

// Clock abstracts time so Pulse never calls time.Now() directly.
// All time reads go through this interface, enabling deterministic testing.
type Clock interface {
	Now() time.Time
}

// realClock wraps time.Now() for production use.
type realClock struct{}

func NewRealClock() Clock {
	return &realClock{}
}

func (c *realClock) Now() time.Time {
	return time.Now()
}

// fakeClock implements Clock with manual time control for tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(t time.Time) Clock {
	return &fakeClock{now: t}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
