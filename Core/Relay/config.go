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

	defaultHandshakeTimeout    = 10 * time.Second
	defaultReadTimeout         = 30 * time.Second
	defaultWriteTimeout        = 10 * time.Second
	defaultPingInterval        = 15 * time.Second
	defaultReconnectInitial    = 1 * time.Second
	defaultReconnectMax        = 30 * time.Second
	defaultReconnectMultiplier = 2.0
	defaultReconnectJitter     = 500 * time.Millisecond
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

// ClientConfig configures the Core-side control tunnel to a Relay.
// All fields can be left at zero to accept sensible defaults.
type ClientConfig struct {
	// RelayURL is the WebSocket URL of the Relay control endpoint.
	// e.g. wss://relay.example.com/v1/control
	RelayURL string `json:"relay_url,omitempty"`

	// RegistrationToken is the long-lived service credential the Core
	// presents to authenticate with the Relay.
	RegistrationToken string `json:"registration_token,omitempty"`

	// HandshakeTimeout limits the WSS dial + initial registration exchange.
	// Zero means the default (10s).
	HandshakeTimeout time.Duration `json:"handshake_timeout,omitempty"`

	// ReadTimeout is the per-message read deadline for the control connection.
	// After this duration without any message (data, ping, or pong) the
	// connection is considered dead and reconnect begins.
	// Zero means the default (30s).
	ReadTimeout time.Duration `json:"read_timeout,omitempty"`

	// WriteTimeout limits each control message write.
	// Zero means the default (10s).
	WriteTimeout time.Duration `json:"write_timeout,omitempty"`

	// PingInterval controls how often the client sends WebSocket-level ping
	// frames to the Relay as a proactive liveness check.
	// Zero means the default (15s).
	PingInterval time.Duration `json:"ping_interval,omitempty"`

	// ReconnectInitial is the initial backoff interval before the first
	// reconnect attempt. Zero means the default (1s).
	ReconnectInitial time.Duration `json:"reconnect_initial,omitempty"`

	// ReconnectMax is the maximum backoff interval. Zero means the default (30s).
	ReconnectMax time.Duration `json:"reconnect_max,omitempty"`

	// ReconnectMultiplier is the exponential backoff multiplier per attempt.
	// Zero means the default (2.0).
	ReconnectMultiplier float64 `json:"reconnect_multiplier,omitempty"`

	// ReconnectJitter is the maximum random jitter added to each backoff.
	// Zero means the default (500ms).
	ReconnectJitter time.Duration `json:"reconnect_jitter,omitempty"`
}

// DefaultClientConfig returns a ClientConfig with sensible defaults.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		HandshakeTimeout:    defaultHandshakeTimeout,
		ReadTimeout:         defaultReadTimeout,
		WriteTimeout:        defaultWriteTimeout,
		PingInterval:        defaultPingInterval,
		ReconnectInitial:    defaultReconnectInitial,
		ReconnectMax:        defaultReconnectMax,
		ReconnectMultiplier: defaultReconnectMultiplier,
		ReconnectJitter:     defaultReconnectJitter,
	}
}

// ApplyDefaults fills any zero-valued fields with sensible defaults.
func (c *ClientConfig) ApplyDefaults() {
	def := DefaultClientConfig()
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = def.HandshakeTimeout
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = def.ReadTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = def.WriteTimeout
	}
	if c.PingInterval <= 0 {
		c.PingInterval = def.PingInterval
	}
	if c.ReconnectInitial <= 0 {
		c.ReconnectInitial = def.ReconnectInitial
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = def.ReconnectMax
	}
	if c.ReconnectMultiplier <= 0 {
		c.ReconnectMultiplier = def.ReconnectMultiplier
	}
	if c.ReconnectJitter <= 0 {
		c.ReconnectJitter = def.ReconnectJitter
	}
}

// Validate checks that the ClientConfig has the minimum required fields.
func (c *ClientConfig) Validate() error {
	if c.RelayURL == "" {
		return errors.New("relay: ClientConfig.RelayURL is required")
	}
	if c.RegistrationToken == "" {
		return errors.New("relay: ClientConfig.RegistrationToken is required")
	}
	if c.HandshakeTimeout < time.Second {
		return errors.New("relay: HandshakeTimeout must be at least 1s")
	}
	if c.ReadTimeout < time.Second {
		return errors.New("relay: ReadTimeout must be at least 1s")
	}
	if c.WriteTimeout < 100*time.Millisecond {
		return errors.New("relay: WriteTimeout must be at least 100ms")
	}
	if c.PingInterval < time.Second {
		return errors.New("relay: PingInterval must be at least 1s")
	}
	if c.ReconnectInitial < 100*time.Millisecond {
		return errors.New("relay: ReconnectInitial must be at least 100ms")
	}
	if c.ReconnectMax < c.ReconnectInitial {
		return errors.New("relay: ReconnectMax must be >= ReconnectInitial")
	}
	if c.ReconnectMultiplier < 1.0 {
		return errors.New("relay: ReconnectMultiplier must be at least 1.0")
	}
	return nil
}
