package relay

import (
	"encoding/hex"
	"errors"
	"fmt"
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

	defaultUDPListenAddress = "0.0.0.0"
	defaultUDPMaxQueueDepth = 256
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

	// Credentials contains the set of authorized registration token verifiers.
	// Each entry is a hex-encoded SHA-256 hash of an authorized registration
	// token. When non-empty, the Relay verifies incoming Register.Token against
	// these hashes before accepting a registration. When empty, any non-empty
	// token is accepted (backward compatible). Credentials are separate from
	// per-route RouteCredentials.
	Credentials []string `json:"credentials,omitempty"`

	// UDP configures the Relay UDP ingress path (per-route public endpoints).
	// Zero-valued fields are filled with defaults.
	UDP UDPConfig `json:"udp,omitempty"`
}

// UDPConfig configures the Relay UDP ingress path. Each open relay route
// gets its own public UDP endpoint; datagrams arriving on it are delivered
// opaque to the owning Core registration's packet sink.
type UDPConfig struct {
	// ListenAddress is the interface per-route UDP listeners bind to.
	// Empty means all interfaces (default "0.0.0.0").
	ListenAddress string `json:"listen_address,omitempty"`

	// PortMin and PortMax bound the range of public UDP ports allocatable to
	// routes. Both zero means ephemeral (OS-assigned) ports. PortMax must be
	// >= PortMin when either is set.
	PortMin int `json:"port_min,omitempty"`
	PortMax int `json:"port_max,omitempty"`

	// MaxPacketSize is the largest accepted UDP payload, in bytes. Datagrams
	// larger than this are rejected with a drop counter. Zero means the
	// default (MaxFramePayloadSize = 65535). Values above MaxFramePayloadSize
	// are invalid.
	MaxPacketSize int `json:"max_packet_size,omitempty"`

	// MaxQueueDepth is the per-route bounded receive queue depth, in
	// datagrams. When a route's queue is full, incoming datagrams are dropped
	// and counted (dropped_queue_full). Zero means the default (256).
	MaxQueueDepth int `json:"max_queue_depth,omitempty"`
}

// DefaultServiceConfig returns a ServiceConfig with sensible defaults.
func DefaultServiceConfig() ServiceConfig {
	cfg := ServiceConfig{
		MaxRegistrations:    defaultMaxRegistrations,
		MaxRoutes:           defaultMaxRoutes,
		KeepaliveInterval:   defaultKeepaliveInterval,
		RouteTimeout:        defaultRouteTimeout,
		RegistrationTimeout: defaultRegistrationTimeout,
	}
	cfg.UDP.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills any zero-valued UDP fields with sensible defaults.
func (c *UDPConfig) ApplyDefaults() {
	if c.ListenAddress == "" {
		c.ListenAddress = defaultUDPListenAddress
	}
	if c.MaxPacketSize == 0 {
		c.MaxPacketSize = MaxFramePayloadSize
	}
	if c.MaxQueueDepth == 0 {
		c.MaxQueueDepth = defaultUDPMaxQueueDepth
	}
}

// Validate checks the UDP config. A zero MaxPacketSize / MaxQueueDepth is
// valid (it means "default").
func (c *UDPConfig) Validate() error {
	if c.MaxPacketSize < 0 || c.MaxPacketSize > MaxFramePayloadSize {
		return fmt.Errorf("relay: UDP.MaxPacketSize must be between 0 and %d", MaxFramePayloadSize)
	}
	if c.MaxQueueDepth < 0 {
		return errors.New("relay: UDP.MaxQueueDepth must not be negative")
	}
	if c.PortMin < 0 || c.PortMax < 0 {
		return errors.New("relay: UDP port range must not be negative")
	}
	if c.PortMin != 0 && c.PortMax != 0 && c.PortMax < c.PortMin {
		return errors.New("relay: UDP.PortMax must be >= UDP.PortMin")
	}
	return nil
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
	// Validate credentials: each must be a 64-char hex-encoded SHA-256 hash.
	if err := validateCredentials(c.Credentials); err != nil {
		return err
	}
	if err := c.UDP.Validate(); err != nil {
		return err
	}
	return nil
}

// validateCredentials checks each credential for valid hex format and length.
func validateCredentials(creds []string) error {
	for _, cred := range creds {
		if len(cred) != 64 {
			return fmt.Errorf("relay: credential %q is not a valid SHA-256 hash (expected 64 hex chars)", cred)
		}
		if _, err := hex.DecodeString(cred); err != nil {
			return fmt.Errorf("relay: credential %q is not valid hex: %w", cred, err)
		}
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
