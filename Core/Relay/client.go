package relay

import (
	"context"
)

// RelayClient defines the Core-side contract for communicating with a
// Doll Relay over an authenticated WebSocket tunnel.
type RelayClient interface {
	// Connect establishes the WebSocket connection to the Relay server
	// and completes the register/registered handshake. Blocks until
	// registered or error.
	Connect(ctx context.Context, url string, token string) error

	// Register performs the authentication handshake over an existing
	// connection. Usually called by Connect but exposed for testing.
	Register(ctx context.Context, token string) error

	// OpenRoute requests a relay route with the given route-scoped
	// credentials. On success the Relay allocates a public UDP endpoint
	// for opaque WireGuard datagrams on this route.
	OpenRoute(ctx context.Context, routeID RouteID, creds RouteCredentials) error

	// CloseRoute terminates a relay route.
	CloseRoute(ctx context.Context, routeID RouteID) error

	// SendPacket sends a WireGuard packet over an open route.
	SendPacket(ctx context.Context, routeID RouteID, data []byte) error

	// RecvPacket returns a channel of received packets. Each packet
	// includes the route ID it arrived on.
	RecvPacket(ctx context.Context) <-chan Packet

	// ErrChan returns a channel of asynchronous protocol errors.
	ErrChan() <-chan error

	// Close terminates the relay connection.
	Close() error
}

// Packet represents a received relayed packet.
type Packet struct {
	RouteID RouteID
	Data    []byte
}
