package relay

import (
	"context"
)

// RelayClient defines the Core-side contract for communicating with a
// Doll Relay over an authenticated WebSocket control tunnel.
//
// M4.3 scope: outbound WSS/TLS control tunnel with registration, route
// management, and automatic reconnect. No datagram forwarding yet.
type RelayClient interface {
	// Start dials the Relay and completes the register/registered handshake.
	// Returns once the control tunnel is established and authenticated.
	// Subsequent calls are idempotent.
	Start(ctx context.Context) error

	// RelayID returns the Relay's identity after successful registration.
	RelayID() RelayID

	// IsConnected reports whether the control tunnel is currently established.
	IsConnected() bool

	// OpenRoute requests a relay route with route-scoped credentials.
	// The allocated endpoint is returned on success.
	OpenRoute(ctx context.Context, routeID RouteID, creds RouteCredentials) (*RouteOpened, error)

	// CloseRoute terminates a relay route.
	CloseRoute(ctx context.Context, routeID RouteID) error

	// Metrics returns accumulated counters for reconnect and restoration.
	Metrics() ControlClientMetrics

	// Shutdown tears down the control tunnel and cancels all pending
	// operations. Blocks until the reconnect loop exits.
	Shutdown() error
}

// ControlClient implements RelayClient.
var _ RelayClient = (*ControlClient)(nil)
