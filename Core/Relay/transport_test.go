// Package relay — M4.5 WireGuard Packet Transport tests.
//
// Proof: byte/datagram identity, RouteID residency, parse/print symmetry,
// bounded queue behavior, failure modes, identity invariants.
package relay

import (
	"bytes"
	"context"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.zx2c4.com/wireguard/conn"
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
		"relay:0",                    // route ID 0 is the "no route" sentinel
		"relay:9223372036854775808",  // MaxRouteID+1 = 2^63
		"relay:18446744073709551615", // uint64 max
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

	const routeID RouteID = 42
	if err := q.Push(routeID, []byte{1, 2, 3}); err != nil {
		t.Fatalf("push: %v", err)
	}

	gotRoute, pkt, err := q.Pop()
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if gotRoute != routeID {
		t.Fatalf("routeID: expected %d, got %d", routeID, gotRoute)
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
	if q.Push(1, []byte{1}) != nil {
		t.Fatalf("first push must succeed")
	}
	if q.Push(2, []byte{2}) != nil {
		t.Fatalf("second push must succeed")
	}
	if q.Push(3, []byte{3}) == nil {
		t.Error("third push must be rejected (queue full)")
	}
}

func TestPacketQueueCloseStopsPushes(t *testing.T) {
	q := NewPacketQueue(5)
	q.Close()
	if q.Push(5, []byte{99}) == nil {
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

	q.Push(1, a)
	q.Push(2, b)

	routeA, gotA, _ := q.Pop()
	routeB, gotB, _ := q.Pop()

	if routeA != 1 || routeB != 2 {
		t.Fatalf("routeIDs not preserved: A=%d B=%d", routeA, routeB)
	}
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
	q.Push(1, original)

	route, got, err := q.Pop()
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if route != 1 {
		t.Fatalf("routeID: expected 1, got %d", route)
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

// fakeEndpoint is a non-relay conn.Endpoint used to prove Send rejects
// endpoints that are not RelayEndpoints (no silent route-0 fallback).
type fakeEndpoint struct{}

func (f *fakeEndpoint) ClearSrc()           {}
func (f *fakeEndpoint) SrcToString() string { return "fake:src" }
func (f *fakeEndpoint) DstToString() string { return "fake:dst" }
func (f *fakeEndpoint) DstToBytes() []byte  { return []byte{1, 2, 3, 4} }
func (f *fakeEndpoint) DstIP() netip.Addr   { return netip.Addr{} }
func (f *fakeEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// ── RouteID Preservation Through Inbound WG Delivery (M4.5 Blocker 1) ──────

// framePumpRelayHandler is a fake Relay that (a) completes registration,
// (b) forwards binary frames it receives from the client to outCh, and
// (c) can inject binary frames to the client from inCh. This exercises the
// real readLoop → frameHandler → queue → conn.ReceiveFunc path.
func framePumpRelayHandler(t *testing.T, inCh <-chan []byte, outCh chan<- []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Registration exchange.
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := UnmarshalControl(raw)
		if err != nil {
			return
		}
		reg, ok := msg.(*Register)
		if !ok || reg.Token == "" {
			return
		}
		data, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("frame-pump-relay"),
		})
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}

		// Writer: inject frames from the test into the client.
		go func() {
			for frame := range inCh {
				if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					return
				}
			}
		}()

		// Reader: capture client→relay binary frames.
		for {
			mt, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			select {
			case outCh <- raw:
			default: // non-blocking; test polls with timeout
			}
		}
	}
}

func TestRelayTransport_PreservesRouteID_ThroughInboundDelivery(t *testing.T) {
	inCh := make(chan []byte, 1)
	outCh := make(chan []byte, 8)

	srv, url := startFakeRelay(t, framePumpRelayHandler(t, inCh, outCh))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "route-token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer client.Shutdown()
	waitConnected(t, client, "route-preserve")

	tr := NewRelayTransport(client)
	recvFns, _, err := tr.Open(0)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer tr.Close()

	// Frame(route=42, payload=X) arrives from the Relay as a binary message.
	payloadX := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}
	inbound := &Frame{Version: ProtocolVersion, RouteID: 42, Payload: payloadX}
	wire, err := MarshalFrame(inbound)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}
	inCh <- wire

	// Drive conn.ReceiveFunc until the datagram shows up (queue Pop blocks).
	var (
		packets = make([][]byte, 1)
		sizes   = make([]int, 1)
		eps     = make([]conn.Endpoint, 1)
	)
	// Provide a real buffer: the WG contract requires the receive func to
	// copy the datagram into the preallocated buffer it is given.
	packets[0] = make([]byte, 4096)
	type recvResult struct {
		n   int
		err error
	}
	recvCh := make(chan recvResult, 1)
	go func() {
		n, err := recvFns[0](packets, sizes, eps)
		recvCh <- recvResult{n: n, err: err}
	}()
	var res recvResult
	select {
	case res = <-recvCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for inbound datagram")
	}
	if res.err != nil {
		t.Fatalf("ReceiveFunc: %v", res.err)
	}
	if res.n != 1 {
		t.Fatalf("ReceiveFunc returned n=%d, want 1", res.n)
	}

	// Byte-identical payload X.
	gotPayload := packets[0][:sizes[0]]
	if !bytes.Equal(gotPayload, payloadX) {
		t.Fatalf("payload mismatch: got %x, want %x", gotPayload, payloadX)
	}

	// Endpoint is relay:42 — the actual inbound RouteID, not routeID 0.
	rep, ok := eps[0].(*RelayEndpoint)
	if !ok {
		t.Fatalf("endpoint type = %T, want *RelayEndpoint", eps[0])
	}
	if rep.getRouteID() != 42 {
		t.Fatalf("endpoint routeID = %d, want 42", rep.getRouteID())
	}
	if eps[0].DstToString() != "relay:42" {
		t.Fatalf("endpoint string = %q, want relay:42", eps[0].DstToString())
	}

	// Send a response via that returned endpoint → outbound frame RouteID=42.
	response := []byte{0xaa, 0xbb, 0xcc}
	if err := tr.Send([][]byte{response}, eps[0]); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case outRaw := <-outCh:
		frame, err := UnmarshalFrame(outRaw)
		if err != nil {
			t.Fatalf("UnmarshalFrame(outbound): %v", err)
		}
		if frame.RouteID != 42 {
			t.Fatalf("outbound frame RouteID = %d, want 42", frame.RouteID)
		}
		if !bytes.Equal(frame.Payload, response) {
			t.Fatalf("outbound payload mismatch: got %x, want %x", frame.Payload, response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for outbound frame")
	}
}

func TestRelayTransport_QueueFullIsObservable(t *testing.T) {
	inCh := make(chan []byte, 8)
	outCh := make(chan []byte, 8)

	srv, url := startFakeRelay(t, framePumpRelayHandler(t, inCh, outCh))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "route-token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer client.Shutdown()
	waitConnected(t, client, "queue-full")

	tr := NewRelayTransport(client)
	if _, _, err := tr.Open(0); err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer tr.Close()

	// Shrink the queue to a tiny capacity to exercise overflow deterministically.
	tr.queue = NewPacketQueue(2)

	mk := func(route RouteID) []byte {
		wire, err := MarshalFrame(&Frame{Version: ProtocolVersion, RouteID: route, Payload: []byte("x")})
		if err != nil {
			t.Fatalf("MarshalFrame: %v", err)
		}
		return wire
	}
	inCh <- mk(1)
	inCh <- mk(2)

	// Wait until the queue is full (both frames consumed by the handler).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tr.queue.Len() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tr.queue.Len() != 2 {
		t.Fatalf("queue depth = %d, want 2 (frames not enqueued)", tr.queue.Len())
	}

	// Third frame must be deterministically rejected and observable.
	inCh <- mk(3)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.FrameHandlerErrors() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if errs := client.FrameHandlerErrors(); errs == 0 {
		t.Fatal("frame handler error not observed after queue overflow")
	} else if tr.queue.Len() != 2 {
		t.Fatalf("queue depth changed on overflow: got %d, want 2", tr.queue.Len())
	}

	// The rejected frame never reached the queue; remaining frames still pop
	// with their RouteIDs intact.
	routeA, _, err := tr.queue.Pop()
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if routeA != 1 {
		t.Fatalf("first queued routeID = %d, want 1", routeA)
	}
	routeB, _, err := tr.queue.Pop()
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if routeB != 2 {
		t.Fatalf("second queued routeID = %d, want 2", routeB)
	}
}

func TestRelayTransport_Send_RejectsNonRelayEndpoint(t *testing.T) {
	inCh := make(chan []byte, 1)
	outCh := make(chan []byte, 8)

	srv, url := startFakeRelay(t, framePumpRelayHandler(t, inCh, outCh))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "route-token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer client.Shutdown()
	waitConnected(t, client, "send-reject")

	tr := NewRelayTransport(client)
	if _, _, err := tr.Open(0); err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer tr.Close()

	// A non-relay endpoint must be rejected, not silently routed with route 0.
	err := tr.Send([][]byte{{0x01}}, &fakeEndpoint{})
	if err != ErrEndpointType {
		t.Fatalf("Send with non-relay endpoint: got %v, want ErrEndpointType", err)
	}
}
