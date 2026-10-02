package relay

import (
	"encoding/binary"
	"testing"
)

func TestMarshalFrame_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		f    *Frame
	}{
		{
			name: "happy path — small payload",
			f: &Frame{
				Version: 1,
				RouteID: 1,
				Payload: []byte("hello"),
			},
		},
		{
			name: "route ID at max",
			f: &Frame{
				Version: 1,
				RouteID: MaxRouteID,
				Payload: []byte{0x01, 0x02},
			},
		},
		{
			name: "empty payload",
			f: &Frame{
				Version: 1,
				RouteID: 42,
				Payload: nil,
			},
		},
		{
			name: "medium payload",
			f: &Frame{
				Version: 1,
				RouteID: 100,
				Payload: make([]byte, 1024),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := MarshalFrame(tt.f)
			if err != nil {
				t.Fatalf("MarshalFrame: %v", err)
			}

			got, err := UnmarshalFrame(data)
			if err != nil {
				t.Fatalf("UnmarshalFrame: %v", err)
			}

			if got.Version != tt.f.Version {
				t.Errorf("Version = %d, want %d", got.Version, tt.f.Version)
			}
			if got.RouteID != tt.f.RouteID {
				t.Errorf("RouteID = %d, want %d", got.RouteID, tt.f.RouteID)
			}
			if len(got.Payload) != len(tt.f.Payload) {
				t.Fatalf("Payload length = %d, want %d", len(got.Payload), len(tt.f.Payload))
			}
			for i, b := range got.Payload {
				if b != tt.f.Payload[i] {
					t.Fatalf("Payload byte %d = %02x, want %02x", i, b, tt.f.Payload[i])
				}
			}
		})
	}
}

func TestMarshalFrame_Errors(t *testing.T) {
	t.Parallel()

	t.Run("nil frame", func(t *testing.T) {
		_, err := MarshalFrame(nil)
		if err == nil {
			t.Fatal("expected error for nil frame")
		}
	})

	t.Run("bad version", func(t *testing.T) {
		_, err := MarshalFrame(&Frame{Version: 99, RouteID: 1})
		if err == nil {
			t.Fatal("expected error for bad version")
		}
	})

	t.Run("route ID zero", func(t *testing.T) {
		_, err := MarshalFrame(&Frame{Version: 1, RouteID: 0})
		if err == nil {
			t.Fatal("expected error for route ID 0")
		}
	})

	t.Run("route ID above max", func(t *testing.T) {
		_, err := MarshalFrame(&Frame{Version: 1, RouteID: MaxRouteID + 1})
		if err == nil {
			t.Fatal("expected error for over-max route ID")
		}
	})

	t.Run("oversized payload", func(t *testing.T) {
		_, err := MarshalFrame(&Frame{
			Version: 1,
			RouteID: 1,
			Payload: make([]byte, MaxFramePayloadSize+1),
		})
		if err == nil {
			t.Fatal("expected error for oversized payload")
		}
	})
}

func TestUnmarshalFrame_Errors(t *testing.T) {
	t.Parallel()

	t.Run("too short", func(t *testing.T) {
		_, err := UnmarshalFrame([]byte{0x01, 0x02, 0x03})
		if err == nil {
			t.Fatal("expected error for too-short data")
		}
	})

	t.Run("bad version", func(t *testing.T) {
		data := make([]byte, FrameHeaderSize)
		data[0] = 99
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for bad version")
		}
	})

	t.Run("route ID zero", func(t *testing.T) {
		data := make([]byte, FrameHeaderSize)
		data[0] = 1
		// route ID bytes 1-9 are zero by default
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for route ID 0 (inbound)")
		}
	})

	t.Run("route ID above max", func(t *testing.T) {
		data := make([]byte, FrameHeaderSize)
		data[0] = 1
		// Set route ID to MaxRouteID+1 = 2^63
		data[1] = 0x80
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for over-max route ID (inbound)")
		}
	})

	t.Run("oversized payload — length mismatch", func(t *testing.T) {
		// Declare a full uint16 length but provide only 2 bytes of data
		data := make([]byte, FrameHeaderSize+2)
		data[0] = 1
		data[8] = 1
		// Set length to 65535 (max uint16, within format range)
		binary.BigEndian.PutUint16(data[9:11], 65535)
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for length mismatch")
		}
	})

	t.Run("length mismatch — truncated data", func(t *testing.T) {
		data := make([]byte, FrameHeaderSize+2)
		data[0] = 1
		// route ID = 1
		data[8] = 1
		// payload length = 1000 but only 2 bytes follow
		binary.BigEndian.PutUint16(data[9:11], 1000)
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for length mismatch")
		}
	})

	t.Run("length mismatch — extra data", func(t *testing.T) {
		// header says 2 bytes but we supply 3
		data := make([]byte, FrameHeaderSize+3)
		data[0] = 1
		data[8] = 1
		binary.BigEndian.PutUint16(data[9:11], 2)
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Fatal("expected error for length mismatch")
		}
	})
}
