package relay

// RelayID identifies a Relay instance within a Doll Network.
type RelayID string

// RouteID identifies a single relay route between Core and a remote peer.
type RouteID uint64

// Frame is the binary protocol frame exchanged over the WSS data channel.
// Wire format (big-endian):
//
//	version (1 byte) + route_id (8 bytes) + length (2 bytes BE) + opaque_wg_datagram (length bytes)
//
// Version is always 1.
type Frame struct {
	Version uint8
	RouteID RouteID
	Payload []byte
}

// ControlMessageType identifies the kind of JSON control message.
type ControlMessageType string

const (
	CmdRegister    ControlMessageType = "register"
	CmdRegistered  ControlMessageType = "registered"
	CmdRouteOpen   ControlMessageType = "route_open"
	CmdRouteOpened ControlMessageType = "route_opened"
	CmdRouteClose  ControlMessageType = "route_close"
	CmdRouteClosed ControlMessageType = "route_closed"
	CmdError       ControlMessageType = "error"
)

// Register is sent by Core to authenticate with the Relay.
type Register struct {
	Type  ControlMessageType `json:"type"`
	Token string             `json:"token"`
}

// Registered is sent by Relay upon successful authentication.
type Registered struct {
	Type    ControlMessageType `json:"type"`
	RelayID RelayID            `json:"relay_id"`
}

// RouteCredentials carries per-route authentication material.
// This is separate from the Core registration token and from
// any WireGuard or Doll identity.
type RouteCredentials struct {
	Token string `json:"token"`
}

// RouteOpen requests a new relay route for an authenticated Core registration.
// The Core presents route-scoped credentials; the Relay allocates a public UDP
// endpoint for opaque WireGuard datagrams on this route.
type RouteOpen struct {
	Type        ControlMessageType `json:"type"`
	RouteID     RouteID            `json:"route_id"`
	Credentials RouteCredentials   `json:"credentials"`
}

// RouteOpened confirms a route was created. AllocatedEndpoint is the public
// UDP endpoint the Relay has assigned for this route's WireGuard traffic.
// Credentials are the route's independently established credentials that the
// client must present on subsequent operations (e.g. reconnect).
type RouteOpened struct {
	Type              ControlMessageType `json:"type"`
	RouteID           RouteID            `json:"route_id"`
	AllocatedEndpoint string             `json:"endpoint"`
	Credentials       RouteCredentials   `json:"credentials"`
}

// RouteClose requests closing a relay route.
type RouteClose struct {
	Type    ControlMessageType `json:"type"`
	RouteID RouteID            `json:"route_id"`
}

// RouteClosed confirms a route was closed.
type RouteClosed struct {
	Type    ControlMessageType `json:"type"`
	RouteID RouteID            `json:"route_id"`
}

// RelayError is sent by Relay on protocol errors.
// RouteID identifies which route operation failed; zero means not scoped
// (e.g. a registration failure).
type RelayError struct {
	Type    ControlMessageType `json:"type"`
	Code    string             `json:"code"`
	Message string             `json:"message"`
	RouteID RouteID            `json:"route_id,omitempty"`
}

// ErrorCode constants for RelayError.Code.
const (
	ErrRateLimited = "rate_limited"
	ErrAuthFailed  = "auth_failed"
	ErrRouteLimit  = "route_limit"
	ErrNotFound    = "not_found"
	ErrInternal    = "internal_error"
)

// UDPEndpoint is the public UDP address (host:port) allocated to a single
// relay route. Every route gets its own endpoint; endpoints are never shared
// between routes.
type UDPEndpoint string

// String returns the endpoint in host:port form.
func (e UDPEndpoint) String() string { return string(e) }

// SourceEndpoint is the observed source address of an incoming UDP datagram.
// It is runtime topology information only: the relay tracks the last-seen
// source so it can return traffic on the same route. It is NEVER treated as
// a Doll or network identity — identity comes exclusively from the route and
// its owning registration.
type SourceEndpoint struct {
	IP   string
	Port int
}
