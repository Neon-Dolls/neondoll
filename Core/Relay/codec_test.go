package relay

import (
	"testing"
)

func TestMarshalFrame_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		frame *Frame
	}{
		{
			name: "basic wg frame",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 42,
				Type:    FrameTypeWireGuard,
				Payload: []byte{0x01, 0x02, 0x03, 0x04},
			},
		},
		{
			name: "empty payload",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 1,
				Type:    FrameTypeWireGuard,
				Payload: []byte{},
			},
		},
		{
			name: "max route ID",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: MaxRouteID,
				Type:    FrameTypeUnspecified,
				Payload: []byte("hello"),
			},
		},
		{
			name: "large payload",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 100,
				Type:    FrameTypeWireGuard,
				Payload: make([]byte, 4096),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := MarshalFrame(tt.frame)
			if err != nil {
				t.Fatalf("MarshalFrame: %v", err)
			}

			got, err := UnmarshalFrame(data)
			if err != nil {
				t.Fatalf("UnmarshalFrame: %v", err)
			}

			if got.Version != tt.frame.Version {
				t.Errorf("Version: got %d, want %d", got.Version, tt.frame.Version)
			}
			if got.RouteID != tt.frame.RouteID {
				t.Errorf("RouteID: got %d, want %d", got.RouteID, tt.frame.RouteID)
			}
			if string(got.Payload) != string(tt.frame.Payload) {
				t.Errorf("Payload: got %x, want %x", got.Payload, tt.frame.Payload)
			}
			// Type is always WireGuard after unmarshal (not in wire format)
			if got.Type != FrameTypeWireGuard {
				t.Errorf("Type: got %d, want %d", got.Type, FrameTypeWireGuard)
			}
		})
	}
}

func TestMarshalFrame_Errors(t *testing.T) {
	tests := []struct {
		name  string
		frame *Frame
	}{
		{
			name:  "nil frame",
			frame: nil,
		},
		{
			name: "bad version",
			frame: &Frame{
				Version: 99,
				RouteID: 1,
				Type:    FrameTypeWireGuard,
			},
		},
		{
			name: "zero route ID",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 0,
				Type:    FrameTypeWireGuard,
			},
		},
		{
			name: "bad frame type",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 1,
				Type:    255,
			},
		},
		{
			name: "payload too large",
			frame: &Frame{
				Version: ProtocolVersion,
				RouteID: 1,
				Type:    FrameTypeWireGuard,
				Payload: make([]byte, MaxFramePayloadSize+1),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MarshalFrame(tt.frame)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestUnmarshalFrame_Errors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "too small",
			data: make([]byte, FrameHeaderSize-1),
		},
		{
			name: "bad version",
			data: func() []byte {
				b := make([]byte, FrameHeaderSize)
				b[0] = 99
				return b
			}(),
		},
		{
			name: "length mismatch",
			data: func() []byte {
				b := make([]byte, FrameHeaderSize+5)
				b[0] = ProtocolVersion
				binaryWriteUint16(b[9:11], 10) // claims 10 bytes, only 5 available
				return b
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalFrame(tt.data)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// binaryWriteUint16 writes v as big-endian uint16 to buf (must be at least 2 bytes).
func binaryWriteUint16(buf []byte, v uint16) {
	buf[0] = byte(v >> 8)
	buf[1] = byte(v)
}