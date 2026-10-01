// Package relay — M4.5 WireGuard Packet Transport tests.
//
// Proof: byte/datagram identity, RouteID residency, parse/print symmetry,
// bounded queue behavior, failure modes, identity invariants.
package relay

import (
	"testing"
)

// ── RelayEndpoint Tests ──────────────────────────────────────────────────────

func TestRelayEndpointParseRoundTrip(t *testing.T) {
	t.Parallel()

	for _, routeID := range []RouteID{1, 42, 9999, 1<<63 - 1} {
		s := RelayEndpointString(routeID)
		ep, err := ParseRelayEndpoint(s)
		if err != nil {
			t.Fatalf("ParseRelayEndpoint(%q): %v", s, err)
		}
		if ep.getRouteID() != routeID {
			t.Fatalf("routeID mismatch: expected %d, got %d", routeID, ep.getRouteID())
		}
	}
}

func TestRelayEndpointInvalidFormat(t *testing.T) {
	for _, s := range []string{
		"",
		"127.0.0.1:51820",
		"relay:",
		"relay:abc",
		"relay:-1",
	} {
		_, err := ParseRelayEndpoint(s)
		if err == nil {
			t.Errorf("expected error for %v", s)
		}
	}
}

func TestRelayEndpointInterfaceMethods(t *testing.T) {
	s := RelayEndpointString(77)
	ep, err := ParseRelayEndpoint(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Verify Endpoint interface methods are callable.
	ep.ClearSrc()
	_ = ep.SrcToString()

	ds := ep.DstToString()
	if ds != s {
		t.Fatalf("DstToString mismatch: %q != %q", ds, s)
	}

	b := ep.DstToBytes()
	if len(b) != 8 {
		t.Fatalf("DstToBytes length: expected 8, got %d", len(b))
	}
	if b[7] != byte(0x4d) {
		t.Fatalf("DstToBytes last byte: expected 0x4d, got 0x%x", b[7])
	}

	ip := ep.DstIP()
	if !ip.Is6() {
		t.Error("DstIP should be IPv6 (ULA)")
	}
}

// ── PacketQueue Tests ────────────────────────────────────────────────────────

func TestPacketQueueBasicPushPop(t *testing.T) {
	q := NewPacketQueue(10)

	if err := q.Push([]byte{1, 2, 3}); err != nil {
		t.Fatalf("push: %v", err)
	}

	pkt, err := q.Pop()
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if pkt == nil {
		t.Fatalf("pop returned nil on non-empty queue")
	}
	if len(pkt) != 3 || pkt[0] != 1 {
		t.Fatalf("bad packet content")
	}
}

func TestPacketQueueFullRejects(t *testing.T) {
	q := NewPacketQueue(2)
	if q.Push([]byte{1}) != nil {
		t.Fatalf("first push must succeed")
	}
	if q.Push([]byte{2}) != nil {
		t.Fatalf("second push must succeed")
	}
	if q.Push([]byte{3}) == nil {
		t.Error("third push must be rejected (queue full)")
	}
}

func TestPacketQueueCloseStopsPushes(t *testing.T) {
	q := NewPacketQueue(5)
	q.Close()
	if q.Push([]byte{99}) == nil {
		t.Error("push after close must be rejected")
	}
}

// ── Frame Codec Integration Tests ───────────────────────────────────────────

func TestMarshalFrameRoundTrip(t *testing.T) {
	t.Parallel()

	payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	f := &Frame{
		Version: ProtocolVersion,
		RouteID: 42,
		Payload: payload,
	}
	data, err := MarshalFrame(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	f2, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if f2.Version != ProtocolVersion {
		t.Fatalf("version mismatch: %d", f2.Version)
	}
	if f2.RouteID != 42 {
		t.Fatalf("routeID: expected 42, got %d", f2.RouteID)
	}
	if len(f2.Payload) != len(payload) {
		t.Fatalf("payload length: expected %d, got %d", len(payload), len(f2.Payload))
	}
	for i := range payload {
		if f2.Payload[i] != payload[i] {
			t.Fatalf("payload byte %d: expected 0x%02x, got 0x%02x", i, payload[i], f2.Payload[i])
		}
	}
}

func TestMarshalFrameOversizedRejected(t *testing.T) {
	big := make([]byte, MaxFramePayloadSize+1)
	_, err := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: 1,
		Payload: big,
	})
	if err == nil {
		t.Error("oversized frame must be rejected")
	}
}

func TestUnmarshalFrameMalformed(t *testing.T) {
	cases := [][]byte{
		[]byte{},                       // too small
		[]byte{0x00, 0x00, 0x00, 0x00}, // short header
		[]byte{0x00},                   // single byte
	}
	for _, data := range cases {
		_, err := UnmarshalFrame(data)
		if err == nil {
			t.Error("malformed frame must be rejected")
		}
	}
}

func TestUnmarshalFrameUnknownRouteFails(t *testing.T) {
	// RouteID = 0 is invalid per types.go validation.
	data := make([]byte, FrameHeaderSize)
	data[0] = ProtocolVersion
	// RouteID bytes (1:9) are zero = invalid, length = 0
	data[9] = 0x00
	data[10] = 0x00

	_, err := UnmarshalFrame(data)
	if err == nil {
		t.Error("RouteID 0 must be rejected")
	}
}

func TestUnmarshalFrameLengthMismatchFails(t *testing.T) {
	// Header claims payload length=1 but data has no payload bytes.
	data := make([]byte, FrameHeaderSize)
	data[0] = ProtocolVersion
	data[1] = 0x00
	data[2] = 0x00
	data[3] = 0x00
	data[4] = 0x00
	data[5] = 0x00
	data[6] = 0x00
	data[7] = 0x00
	data[8] = 0x01 // RouteID = 1
	data[9] = 0x00
	data[10] = 0x01 // length claims 1 byte of payload, but none present

	// data is only FrameHeaderSize bytes, but header says FrameHeaderSize + 1.
	_, err := UnmarshalFrame(data)
	if err == nil {
		t.Error("length mismatch must be rejected")
	}
}

func TestUnmarshalFrameBadVersion(t *testing.T) {
	data := make([]byte, FrameHeaderSize)
	data[0] = 0xff

	_, err := UnmarshalFrame(data)
	if err == nil {
		t.Error("bad version must be rejected")
	}
}

func TestMarshalFrameNilRejected(t *testing.T) {
	_, err := MarshalFrame(nil)
	if err == nil {
		t.Error("nil frame must be rejected")
	}
}

// ── Datagram Boundary Tests ────────────────────────────────────────────────

func TestConsecutiveDatagramsRemainDistinct(t *testing.T) {
	q := NewPacketQueue(10)

	a := []byte{0xaa, 0xbb}
	b := []byte{0xcc, 0xdd, 0xee}

	q.Push(a)
	q.Push(b)

	gotA, _ := q.Pop()
	gotB, _ := q.Pop()

	if len(gotA) != 2 || gotA[0] != 0xaa || gotA[1] != 0xbb {
		t.Fatalf("first datagram corrupted")
	}
	if len(gotB) != 3 || gotB[0] != 0xcc || gotB[1] != 0xdd || gotB[2] != 0xee {
		t.Fatalf("second datagram corrupted")
	}
}

func TestPacketQueueByteIdentity(t *testing.T) {
	q := NewPacketQueue(10)
	original := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	q.Push(original)

	got, err := q.Pop()
	if err != nil {
		t.Fatalf("pop: %v", err)
	}

	if len(original) != len(got) {
		t.Fatalf("length mismatch: %d vs %d", len(original), len(got))
	}
	for i := range original {
		if original[i] != got[i] {
			t.Fatalf("byte %d: expected 0x%02x, got 0x%02x", i, original[i], got[i])
		}
	}
}

// ── Identity Invariant Tests ───────────────────────────────────────────────

func TestRelayEndpointHasNoIdentity(t *testing.T) {
	t.Parallel()

	ep, err := ParseRelayEndpoint("relay:7")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// A RelayEndpoint carries only RouteID — not WG key, peer identity,
	// NetworkID, or Doll identity.
	if ep.getRouteID() != 7 {
		t.Fatalf("routeID preserved: expected 7, got %d", ep.getRouteID())
	}

	// The struct has no fields for identity material.
	// This is enforced by the type definition itself.
}
