package interaction

import (
	"context"
	"sync"
)

type keyedLock struct {
	token chan struct{}
	refs  int
}

// keyedLocks serializes work per key while allowing unrelated keys to proceed.
// Entries are reference-counted across owners and waiters and removed when the
// last reference releases or cancels, so the map cannot grow with old sessions.
type keyedLocks struct {
	mu      sync.Mutex
	entries map[string]*keyedLock
}

func (locks *keyedLocks) acquire(ctx context.Context, key string) (func(), error) {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*keyedLock)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &keyedLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		locks.entries[key] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	select {
	case <-entry.token:
		var once sync.Once
		return func() {
			once.Do(func() {
				entry.token <- struct{}{}
				locks.drop(key, entry)
			})
		}, nil
	case <-ctx.Done():
		locks.drop(key, entry)
		return nil, ctx.Err()
	}
}

func (locks *keyedLocks) drop(key string, entry *keyedLock) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && locks.entries[key] == entry {
		delete(locks.entries, key)
	}
}
