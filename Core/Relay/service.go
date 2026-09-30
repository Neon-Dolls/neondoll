package relay

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ServiceState represents the lifecycle state of the relay service.
type ServiceState int

const (
	ServiceStateStopped ServiceState = iota
	ServiceStateStarting
	ServiceStateRunning
	ServiceStateStopping
	ServiceStateStoppedClean
)

func (s ServiceState) String() string {
	switch s {
	case ServiceStateStopped:
		return "stopped"
	case ServiceStateStarting:
		return "starting"
	case ServiceStateRunning:
		return "running"
	case ServiceStateStopping:
		return "stopping"
	case ServiceStateStoppedClean:
		return "stopped_clean"
	default:
		return fmt.Sprintf("ServiceState(%d)", int(s))
	}
}

// Service is the relay service skeleton. It manages configuration, the route
// registry, liveness checks, and diagnostic metrics. Transport-level concerns
// (UDP forwarding, WSS tunnels, WireGuard packet injection) are deferred to
// M4.3+ — this skeleton provides the control-plane foundation.
type Service struct {
	config   ServiceConfig
	registry *Registry
	metrics  *Metrics

	mu         sync.Mutex
	state      ServiceState
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	livenessWg sync.WaitGroup
}

// NewService creates a relay Service with the given config.
func NewService(cfg ServiceConfig) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("relay: invalid config: %w", err)
	}
	return &Service{
		config:   cfg,
		registry: NewRegistry(cfg.MaxRegistrations, cfg.MaxRoutes),
		metrics:  NewMetrics(),
		state:    ServiceStateStopped,
	}, nil
}

// Start launches the relay service. It begins the liveness-check loop and
// transitions the service to Running. Start returns when the service is
// ready to accept registrations/routes. The provided context is used for
// cancellation — call Shutdown to stop gracefully.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state != ServiceStateStopped {
		s.mu.Unlock()
		return fmt.Errorf("relay: service already started (state=%s)", s.state)
	}
	s.state = ServiceStateStarting

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	// Start liveness loop
	s.livenessWg.Add(1)
	go s.livenessLoop(ctx)

	s.mu.Lock()
	s.state = ServiceStateRunning
	s.mu.Unlock()
	return nil
}

// Shutdown stops the service gracefully. It stops the liveness loop, expires
// all registrations and routes, and blocks until everything is cleaned up.
// If the context is cancelled before cleanup completes, Shutdown returns the
// context error after attempting to cancel remaining work.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.state != ServiceStateRunning {
		s.mu.Unlock()
		return fmt.Errorf("relay: service not running (state=%s)", s.state)
	}
	s.state = ServiceStateStopping
	s.mu.Unlock()

	// Cancel the liveness loop
	if s.cancel != nil {
		s.cancel()
	}

	// Wait for liveness loop to finish
	done := make(chan struct{})
	go func() {
		s.livenessWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// liveness loop stopped
	case <-ctx.Done():
		return ctx.Err()
	}

	// Expire all registrations and routes
	s.registry.ExpireStale(0, 0) // pass 0 timeouts = expire everything

	s.mu.Lock()
	s.state = ServiceStateStoppedClean
	s.mu.Unlock()
	return nil
}

// State returns the current service state.
func (s *Service) State() ServiceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Registry returns the service's route registry.
func (s *Service) Registry() *Registry {
	return s.registry
}

// Config returns a copy of the service configuration.
func (s *Service) Config() ServiceConfig {
	return s.config
}

// Metrics returns the service's diagnostic counters.
func (s *Service) Metrics() *Metrics {
	return s.metrics
}

// Diagnostics returns a snapshot of service state for monitoring.
func (s *Service) Diagnostics() map[string]any {
	s.mu.Lock()
	state := s.state.String()
	s.mu.Unlock()

	m := s.metrics.Snapshot()
	d := make(map[string]any, len(m)+3)
	d["service_state"] = state
	for k, v := range m {
		d[k] = v
	}
	d["active_registrations"] = s.registry.RegistrationCount()
	d["active_routes"] = s.registry.RouteCount()
	return d
}

// livenessLoop periodically checks for stale registrations and routes.
// It runs until the context is cancelled.
func (s *Service) livenessLoop(ctx context.Context) {
	defer s.livenessWg.Done()

	ticker := time.NewTicker(s.config.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.metrics.incLivenessChecks()
			expired := s.registry.ExpireStale(
				s.config.RegistrationTimeout,
				s.config.RouteTimeout,
			)
			for _, routeIDs := range expired {
				for range routeIDs {
					s.metrics.incStaleRoutesExpired()
				}
				s.metrics.incRegistrationsDropped()
			}
		}
	}
}
