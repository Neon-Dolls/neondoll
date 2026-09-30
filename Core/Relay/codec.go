package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Protocol constants.
const (
	ProtocolVersion     uint8  = 1
	FrameHeaderSize            = 11 // version(1) + route_id(8) + length(2)
	MaxFramePayloadSize        = 65535 // uint16 max
	MaxRouteID          RouteID = 1<<63 - 1 // avoid sign-bit use
)

var (
	ErrFrameTooSmall   = errors.New("relay: frame too small")
	ErrFrameTooLarge   = fmt.Errorf("relay: frame payload exceeds max (%d)", MaxFramePayloadSize)
	ErrFrameBadVersion = errors.New("relay: bad frame version")
	ErrFrameBadType    = errors.New("relay: unknown frame type")
)

// MarshalFrame encodes a Frame into its wire representation.
//
// Wire format (big-endian):
//
//	version (1 byte) + route_id (8 bytes) + length (2 bytes) + payload (N bytes)
//
// FrameType is metadata only and is NOT serialized into the wire format.
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
	if f.Type != FrameTypeUnspecified && f.Type != FrameTypeWireGuard {
		return nil, ErrFrameBadType
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
// The returned Frame always has Type set to FrameTypeWireGuard since the
// wire format does not carry a type discriminator.
func UnmarshalFrame(data []byte) (*Frame, error) {
	if len(data) < FrameHeaderSize {
		return nil, ErrFrameTooSmall
	}

	version := data[0]
	if version != ProtocolVersion {
		return nil, ErrFrameBadVersion
	}

	routeID := RouteID(binary.BigEndian.Uint64(data[1:9]))
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
		Type:    FrameTypeWireGuard,
		Payload: payload,
	}, nil
}
