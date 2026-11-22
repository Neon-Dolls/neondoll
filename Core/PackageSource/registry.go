package packagesource

import (
	"context"
	"sort"
	"sync"
)

type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
	closed  bool
}

func NewRegistry() *Registry { return &Registry{sources: map[string]Source{}} }

func (r *Registry) Register(source Source) error {
	if source == nil || source.ID() == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrSourceClosed
	}
	if r.sources[source.ID()] != nil {
		return ErrConflict
	}
	r.sources[source.ID()] = source
	return nil
}

func (r *Registry) Unregister(ctx context.Context, id string) error {
	r.mu.Lock()
	source := r.sources[id]
	delete(r.sources, id)
	r.mu.Unlock()
	if source == nil {
		return nil
	}
	return source.Close(ctx)
}

func (r *Registry) Source(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.sources[id]
	return source, ok
}

func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.sources))
	for id := range r.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sources := r.sources
	r.sources = map[string]Source{}
	r.mu.Unlock()
	var first error
	for _, source := range sources {
		if err := source.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
