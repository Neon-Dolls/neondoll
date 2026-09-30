package relay

// RelayID identifies a Relay instance within a Doll Network.
type RelayID string

// RouteID identifies a single relay route between Core and a remote peer.
type RouteID uint64

// FrameType indicates whether a binary frame carries a WireGuard packet or other payload.
type FrameType uint8

const (
	FrameTypeUnspecified FrameType = 0
	FrameTypeWireGuard   FrameType = 1
)

// Frame is the binary protocol frame exchanged over the WSS data channel.
// Wire format (big-endian):
//
//	version (1 byte) + route_id (8 bytes) + length (2 bytes BE) + payload (length bytes)
//
// Version is always 1.
type Frame struct {
	Version uint8
	RouteID RouteID
	Type    FrameType
	Payload []byte
}

// ControlMessageType identifies the kind of JSON control message.
type ControlMessageType string

const (
	CmdRegister     ControlMessageType = "register"
	CmdRegistered   ControlMessageType = "registered"
	CmdRouteOpen    ControlMessageType = "route_open"
	CmdRouteOpened  ControlMessageType = "route_opened"
	CmdRouteClose   ControlMessageType = "route_close"
	CmdRouteClosed  ControlMessageType = "route_closed"
	CmdError        ControlMessageType = "error"
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

// RouteOpen requests a new relay route.
type RouteOpen struct {
	Type       ControlMessageType `json:"type"`
	RouteID    RouteID            `json:"route_id"`
	SourceAddr string             `json:"source_addr"`
	TargetAddr string             `json:"target_addr"`
}

// RouteOpened confirms a route was created.
type RouteOpened struct {
	Type       ControlMessageType `json:"type"`
	RouteID    RouteID            `json:"route_id"`
	SourceAddr string             `json:"source_addr"`
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
type RelayError struct {
	Type    ControlMessageType `json:"type"`
	Code    string             `json:"code"`
	Message string             `json:"message"`
}

// ErrorCode constants for RelayError.Code.
const (
	ErrRateLimited = "rate_limited"
	ErrAuthFailed  = "auth_failed"
	ErrRouteLimit  = "route_limit"
	ErrNotFound    = "not_found"
	ErrInternal    = "internal_error"
)
