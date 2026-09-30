package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Protocol constants.
const (
	ProtocolVersion       uint8   = 1
	FrameHeaderSize               = 11        // version(1) + route_id(8) + length(2)
	MaxFramePayloadSize           = 65535     // uint16 max
	MaxRouteID            RouteID = 1<<63 - 1 // avoid sign-bit use
	MaxControlMessageSize         = 65536     // 64 KB — maximum control message body size
)

var (
	ErrFrameTooSmall   = errors.New("relay: frame too small")
	ErrFrameTooLarge   = fmt.Errorf("relay: frame payload exceeds max (%d)", MaxFramePayloadSize)
	ErrFrameBadVersion = errors.New("relay: bad frame version")
	ErrControlTooLarge = fmt.Errorf("relay: control message exceeds max (%d bytes)", MaxControlMessageSize)
)

// MarshalFrame encodes a Frame into its wire representation.
//
// Wire format (big-endian):
//
//	version (1 byte) + route_id (8 bytes) + length (2 bytes) + opaque_wg_datagram (N bytes)
func MarshalFrame(f *Frame) ([]byte, error) {
	if f == nil {
		return nil, errors.New("relay: nil frame")
	}
	if f.Version != ProtocolVersion {
		return nil, ErrFrameBadVersion
	}
	if f.RouteID == 0 || f.RouteID > MaxRouteID {
		return nil, errors.New("relay: invalid route ID")
	}
	if len(f.Payload) > MaxFramePayloadSize {
		return nil, ErrFrameTooLarge
	}

	buf := make([]byte, FrameHeaderSize+len(f.Payload))
	buf[0] = f.Version
	binary.BigEndian.PutUint64(buf[1:9], uint64(f.RouteID))
	binary.BigEndian.PutUint16(buf[9:11], uint16(len(f.Payload)))
	copy(buf[11:], f.Payload)
	return buf, nil
}

// UnmarshalFrame decodes wire bytes into a Frame.
//
// Wire format (big-endian):
//
//	version (1 byte) + route_id (8 bytes) + length (2 bytes) + opaque_wg_datagram (N bytes)
//
// The returned Frame is always an opaque WireGuard datagram for the identified route.
func UnmarshalFrame(data []byte) (*Frame, error) {
	if len(data) < FrameHeaderSize {
		return nil, ErrFrameTooSmall
	}

	version := data[0]
	if version != ProtocolVersion {
		return nil, ErrFrameBadVersion
	}

	routeID := RouteID(binary.BigEndian.Uint64(data[1:9]))
	if routeID == 0 || routeID > MaxRouteID {
		return nil, errors.New("relay: invalid route ID")
	}

	length := binary.BigEndian.Uint16(data[9:11])

	if int(length) > MaxFramePayloadSize {
		return nil, ErrFrameTooLarge
	}
	if len(data) != FrameHeaderSize+int(length) {
		return nil, fmt.Errorf("relay: frame length mismatch: header claims %d bytes, data has %d",
			FrameHeaderSize+int(length), len(data))
	}

	payload := make([]byte, length)
	copy(payload, data[11:])

	return &Frame{
		Version: version,
		RouteID: routeID,
		Payload: payload,
	}, nil
}
