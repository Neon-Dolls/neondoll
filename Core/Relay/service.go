package relay

import (
	"context"
	"errors"
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

// Service is the relay service. It manages configuration, the route
// registry, liveness checks, diagnostic metrics, and an optional
// outbound control tunnel to a Relay.
type Service struct {
	config   ServiceConfig
	registry *Registry
	metrics  *Metrics
	udp      *UDPListener

	clientCfg ClientConfig
	client    *ControlClient

	mu         sync.Mutex
	state      ServiceState
	cancel     context.CancelFunc
	livenessWg sync.WaitGroup
}

// NewService creates a relay Service with the given config and optional
// client configuration. When clientCfg has a RelayURL, Start will
// establish an outbound control tunnel to that Relay.
func NewService(cfg ServiceConfig, clientCfg ClientConfig) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("relay: invalid config: %w", err)
	}
	cfg.UDP.ApplyDefaults()
	registry := NewRegistry(cfg.MaxRegistrations, cfg.MaxRoutes)
	metrics := NewMetrics()
	return &Service{
		config:    cfg,
		clientCfg: clientCfg,
		registry:  registry,
		metrics:   metrics,
		udp:       NewUDPListener(cfg.UDP, registry, metrics),
		state:     ServiceStateStopped,
	}, nil
}

// Start launches the relay service. It begins the liveness-check loop,
// and if a RelayURL is configured, establishes the outbound control tunnel.
// The provided context is used for cancellation — call Shutdown to stop.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state != ServiceStateStopped && s.state != ServiceStateStoppedClean {
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

	// Start control tunnel if configured
	if s.clientCfg.RelayURL != "" {
		client := NewControlClient(s.clientCfg)
		if err := client.Start(ctx); err != nil {
			// Cancel liveness loop if tunnel fails
			cancel()
			s.livenessWg.Wait()
			s.mu.Lock()
			s.state = ServiceStateStopped
			s.mu.Unlock()
			return fmt.Errorf("relay: start control tunnel: %w", err)
		}
		s.client = client
	}

	s.mu.Lock()
	s.state = ServiceStateRunning
	s.mu.Unlock()
	return nil
}

// Shutdown stops the service gracefully. It stops the control tunnel
// (if established), stops the liveness loop, expires all registrations
// and routes, and blocks until everything is cleaned up.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.state != ServiceStateRunning {
		s.mu.Unlock()
		return fmt.Errorf("relay: service not running (state=%s)", s.state)
	}
	s.state = ServiceStateStopping
	s.mu.Unlock()

	// Shutdown control tunnel first if present
	if s.client != nil {
		if err := s.client.Shutdown(); err != nil {
			// Non-fatal — continue with liveness/registry shutdown
			_ = err
		}
	}

	// Shutdown the UDP ingress path: closes every per-route listener and
	// releases all allocated UDP endpoints.
	if err := s.udp.Shutdown(); err != nil {
		// Non-fatal — continue with liveness/registry shutdown
		_ = err
	}

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
	case <-ctx.Done():
		return ctx.Err()
	}

	// Expire all registrations and routes
	s.registry.ExpireStale(0, 0)

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

// Client returns the service's control client, or nil if no tunnel
// was configured.
func (s *Service) Client() *ControlClient {
	return s.client
}

// Metrics returns the service's diagnostic counters.
func (s *Service) Metrics() *Metrics {
	return s.metrics
}

// UDP returns the service's UDP ingress listener.
func (s *Service) UDP() *UDPListener {
	return s.udp
}

// SetPacketSink binds the Core-tunnel boundary (packet sink) for a
// registration. Datagrams arriving on any route owned by the registration
// are delivered to this sink, opaque and byte-identical. M4.4 terminates
// the Core-tunnel side at a test sink; replacing the sink for a
// registration is allowed (the sink itself is not identity).
func (s *Service) SetPacketSink(regID RegistrationID, sink PacketSink) {
	s.udp.SetSink(regID, sink)
}

// OpenRouteEndpoint allocates a public UDP endpoint for an existing route
// and transitions the route to Open. The route must already exist and be
// owned by regID; the returned endpoint is the route's exclusive public
// UDP ingress address. Datagrams arriving there are delivered only to the
// owning registration's packet sink.
func (s *Service) OpenRouteEndpoint(regID RegistrationID, routeID RouteID) (UDPEndpoint, error) {
	if _, err := s.registry.OpenRoute(regID, routeID); err != nil {
		return "", err
	}

	ep, err := s.udp.Bind(routeID)
	if err != nil {
		// Roll back the state transition so the route state stays
		// coherent with its (now failed) endpoint allocation.
		_, _ = s.registry.CloseRoute(regID, routeID)
		return "", err
	}
	if err := s.registry.SetRouteEndpoint(regID, routeID, string(ep)); err != nil {
		_ = s.udp.Close(routeID)
		_, _ = s.registry.CloseRoute(regID, routeID)
		return "", err
	}
	return ep, nil
}

// CloseRouteEndpoint tears down a route's UDP endpoint and closes the
// route. The released UDP port becomes available for reuse by other
// routes immediately.
func (s *Service) CloseRouteEndpoint(regID RegistrationID, routeID RouteID) error {
	if err := s.udp.Close(routeID); err != nil && !errors.Is(err, ErrUDPRouteNotFound) {
		return err
	}
	_, err := s.registry.CloseRoute(regID, routeID)
	return err
}

// Diagnostics returns a snapshot of service state for monitoring.
func (s *Service) Diagnostics() map[string]any {
	s.mu.Lock()
	state := s.state.String()
	s.mu.Unlock()

	m := s.metrics.Snapshot()
	d := make(map[string]any, len(m)+5)
	d["service_state"] = state
	for k, v := range m {
		d[k] = v
	}
	d["active_registrations"] = s.registry.RegistrationCount()
	d["active_routes"] = s.registry.RouteCount()

	// Control tunnel diagnostics
	if s.client != nil {
		d["tunnel_connected"] = s.client.IsConnected()
		d["relay_id"] = string(s.client.RelayID())
		clientMetrics := s.client.Metrics()
		d["tunnel_reconnects"] = clientMetrics.Reconnects
		d["tunnel_routes_restored"] = clientMetrics.RoutesRestored
	} else {
		d["tunnel_connected"] = false
	}

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
			result := s.registry.ExpireStale(
				s.config.RegistrationTimeout,
				s.config.RouteTimeout,
			)
			for range result.StaleRoutes {
				s.metrics.incStaleRoutesExpired()
			}
			for range result.ExpiredRegistrations {
				s.metrics.incRegistrationsDropped()
			}
			for range result.RoutesExpiredViaReg {
				s.metrics.incRoutesExpiredViaRegistration()
			}

			// Close UDP endpoints for all expired routes. A route that never
			// reached endpoint allocation returns ErrUDPRouteNotFound which is
			// harmless — skip it.
			for _, rid := range result.AllExpiredRoutes() {
				if err := s.udp.Close(rid); err != nil && !errors.Is(err, ErrUDPRouteNotFound) {
					s.metrics.incUDPTearDownErrors()
				}
			}
		}
	}
}
