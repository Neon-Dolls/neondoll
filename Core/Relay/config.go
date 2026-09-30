package relay

import (
	"errors"
	"time"
)

const (
	defaultMaxRegistrations    = 256
	defaultMaxRoutes           = 1024
	defaultKeepaliveInterval   = 30 * time.Second
	defaultRouteTimeout        = 120 * time.Second
	defaultRegistrationTimeout = 300 * time.Second
)

// ServiceConfig configures the Relay service behaviour.
type ServiceConfig struct {
	// MaxRegistrations limits the number of concurrent Core registrations.
	// Zero means the default (256).
	MaxRegistrations int `json:"max_registrations,omitempty"`

	// MaxRoutes limits the number of concurrent relay routes across all
	// registrations. Zero means the default (1024).
	MaxRoutes int `json:"max_routes,omitempty"`

	// KeepaliveInterval controls how often the service checks registration
	// and route liveness. Zero means the default (30s).
	KeepaliveInterval time.Duration `json:"keepalive_interval,omitempty"`

	// RouteTimeout is the maximum time since the owning registration's last
	// activity before a route is considered stale and closed. Zero means
	// the default (120s).
	RouteTimeout time.Duration `json:"route_timeout,omitempty"`

	// RegistrationTimeout is the maximum time since the last keepalive before
	// a registration is considered stale and removed. Zero means the default
	// (300s).
	RegistrationTimeout time.Duration `json:"registration_timeout,omitempty"`
}

// DefaultServiceConfig returns a ServiceConfig with sensible defaults.
func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		MaxRegistrations:    defaultMaxRegistrations,
		MaxRoutes:           defaultMaxRoutes,
		KeepaliveInterval:   defaultKeepaliveInterval,
		RouteTimeout:        defaultRouteTimeout,
		RegistrationTimeout: defaultRegistrationTimeout,
	}
}

// Validate checks the config. All numeric/interval fields must be positive.
func (c *ServiceConfig) Validate() error {
	if c.MaxRegistrations < 1 {
		return errors.New("relay: MaxRegistrations must be at least 1")
	}
	if c.MaxRoutes < 1 {
		return errors.New("relay: MaxRoutes must be at least 1")
	}
	if c.KeepaliveInterval < time.Second {
		return errors.New("relay: KeepaliveInterval must be at least 1s")
	}
	if c.RouteTimeout < c.KeepaliveInterval {
		return errors.New("relay: RouteTimeout must be >= KeepaliveInterval")
	}
	if c.RegistrationTimeout < c.KeepaliveInterval {
		return errors.New("relay: RegistrationTimeout must be >= KeepaliveInterval")
	}
	return nil
}
