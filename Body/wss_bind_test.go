// SPDX-License-Identifier: AGPL-3.0-only
// Package body — Body-side WireGuard WSS Bind transport tests (M5.3).

package body

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
	"golang.zx2c4.com/wireguard/conn"
)

// assertPayloadEqual compares two byte slices and fails the test if they differ.
func assertPayloadEqual(t *testing.T, got, want []byte) {
	if len(got) != len(want) {
		t.Errorf("payload len %d != %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("payload differ at byte %d: 0x%02x vs 0x%02x", i, got[i], want[i])
		}
	}
}

// makeFrameBytes marshals a relay Frame with the given payload and RouteID.
func makeFrameBytes(payload []byte, routeID relay.RouteID) ([]byte, error) {
	return relay.MarshalFrame(&relay.Frame{
		Version: relay.ProtocolVersion,
		RouteID: routeID,
		Payload: payload,
	})
}

// testEndpoint is a minimal conn.Endpoint used when calling Send.
type testEndpoint struct{}
func (f *testEndpoint) ClearSrc()           {}
func (f *testEndpoint) SrcToString() string { return "test:src" }
func (f *testEndpoint) DstToString() string { return "test:dst" }
func (f *testEndpoint) DstToBytes() []byte  { return []byte{0} }
func (f *testEndpoint) DstIP() netip.Addr   { return netip.Addr{} }
func (f *testEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// fakeRelayHandler returns an http.HandlerFunc that simulates a Relay /body
// WebSocket endpoint.  It:
//  1. Upgrades the connection.
//  2. Reads the BodyAttach text message.
//  3. Sends a BodyAttached response.
//  4. Spawns a goroutine that sends injected frames (from injectCh) to the
//     connected Bind via the WebSocket (binary messages).
//  5. Writes all binary messages received from the Bind to captureCh.
func fakeRelayHandler(t *testing.T, routeID relay.RouteID, injectCh <-chan []byte, captureCh chan<- []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ug := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, upErr := ug.Upgrade(w, r, nil)
		if upErr != nil {
			t.Errorf("fakeRelay upgrade: %v", upErr)
			return
		}
		defer conn.Close()

		// Read BodyAttach text message.
		mt, raw, rdErr := conn.ReadMessage()
		if rdErr != nil {
			t.Errorf("fakeRelay read BodyAttach: %v", rdErr)
			return
		}
		if mt != websocket.TextMessage {
			t.Errorf("fakeRelay expected TextMessage, got %v", mt)
			return
		}
		attachMsg, unErr := relay.UnmarshalControl(raw)
		if unErr != nil {
			t.Errorf("fakeRelay UnmarshalControl: %v", unErr)
			return
		}
		attach, attachOk := attachMsg.(*relay.BodyAttach)
		if !attachOk {
			t.Errorf("fakeRelay expected BodyAttach, got %T", attachMsg)
			return
		}
		_ = attach // suppress unused warning

		// Send BodyAttached response.
		resp, _ := relay.MarshalControl(&relay.BodyAttached{
			Type:    relay.CmdBodyAttached,
			Version: relay.ProtocolVersion,
			RouteID: routeID,
		})
		if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
			t.Errorf("fakeRelay write BodyAttached: %v", err)
			return
		}

		// Goroutine: inject frames from test.
		go func() {
			for frame := range injectCh {
				if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					return
				}
			}
		}()

		// Main: capture frames from Bind.
		for {
			mt, raw, rErr := conn.ReadMessage()
			if rErr != nil || mt != websocket.BinaryMessage {
				return
			}
			select {
			case captureCh <- raw:
			default:
			}
		}
	}
}

// recvResult is used by receive tests to capture the ReceiveFunc result with
// time.After safety.
type recvResult struct {
	n int
	err error
}

// ── Connect & Attach ─────────────────────────────────────────────────────────
func TestBodyWSSBind_ConnectAttach(t *testing.T) {
	routeID := relay.RouteID(10)
	cred := "test-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 8)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	recvFns, port, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("BodyWSSBind.Open: %v", openErr)
	}
	if len(recvFns) != 1 {
		t.Fatalf("expected 1 receive func, got %d", len(recvFns))
	}
	if port != 0 {
		t.Fatalf("expected port 0, got %d", port)
	}
}

// ── Send Packet ──────────────────────────────────────────────────────────────
func TestBodyWSSBind_SendPacket(t *testing.T) {
	routeID := relay.RouteID(11)
	cred := "test-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 8)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	_, _, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("BodyWSSBind.Open: %v", openErr)
	}

	payload := []byte{0x01, 0x02, 0x03, 0x42}
	sendErr := bind.Send([][]byte{payload}, &testEndpoint{})
	if sendErr != nil {
		t.Fatalf("BodyWSSBind.Send: %v", sendErr)
	}

	// Read the captured frame from the relay handler's capture channel.
	var frame []byte
	select {
	case f := <-captureCh:
		frame = f
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for captured frame")
	}
	if len(frame) < relay.FrameHeaderSize {
		t.Fatalf("captured frame too short: %d bytes", len(frame))
	}
	parsed, parErr := relay.UnmarshalFrame(frame)
	if parErr != nil {
		t.Fatalf("UnmarshalFrame captured frame: %v", parErr)
	}
	assertPayloadEqual(t, parsed.Payload, payload)
	if parsed.RouteID != routeID {
		t.Fatalf("frame RouteID %d != %d", parsed.RouteID, routeID)
	}
}

// ── Receive Packet ───────────────────────────────────────────────────────────
func TestBodyWSSBind_ReceivePacket(t *testing.T) {
	routeID := relay.RouteID(12)
	cred := "test-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 8)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	fns, _, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("BodyWSSBind.Open: %v", openErr)
	}

	// Inject a frame into the Bind's readLoop via the fake relay.
	payload := []byte{0xAA, 0xBB, 0xCC}
	frameBytes, maErr := makeFrameBytes(payload, routeID)
	if maErr != nil {
		t.Fatalf("makeFrameBytes: %v", maErr)
	}
	injectCh <- frameBytes

	// Receive via the returned receive func, with time.After.
	var (
		packets = make([][]byte, 1)
		sizes   = make([]int, 1)
		eps     = make([]conn.Endpoint, 1)
	)
	packets[0] = make([]byte, 4096)

	recvCh := make(chan recvResult, 1)
	go func() {
		count, batchErr := fns[0](packets, sizes, eps)
		recvCh <- recvResult{n: count, err: batchErr}
	}()

	var recvN int
	var recvErr error
	select {
	case res := <-recvCh:
		recvN, recvErr = res.n, res.err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for receive")
	}
	if recvErr != nil {
		t.Fatalf("Receive: %v", recvErr)
	}
	if recvN != 1 {
		t.Fatalf("expected 1 packet, got %d", recvN)
	}
	assertPayloadEqual(t, packets[0][:sizes[0]], payload)
}

// ── Framing ──────────────────────────────────────────────────────────────────
func TestBodyWSSBind_Framing(t *testing.T) {
	routeID := relay.RouteID(13)
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	frameBytes, maErr := makeFrameBytes(payload, routeID)
	if maErr != nil {
		t.Fatalf("makeFrameBytes: %v", maErr)
	}
	if len(frameBytes) < relay.FrameHeaderSize + len(payload) {
		t.Fatalf("frame too short: %d bytes", len(frameBytes))
	}

	parsed, parErr := relay.UnmarshalFrame(frameBytes)
	if parErr != nil {
		t.Fatalf("UnmarshalFrame: %v", parErr)
	}
	if parsed.Version != relay.ProtocolVersion {
		t.Fatalf("version %d != %d", parsed.Version, relay.ProtocolVersion)
	}
	if parsed.RouteID != routeID {
		t.Fatalf("RouteID mismatch")
	}
	assertPayloadEqual(t, parsed.Payload, payload)
}

// ── Malformed Frame ──────────────────────────────────────────────────────────
func TestBodyWSSBind_MalformedFrame(t *testing.T) {
	// Verify that UnmarshalFrame rejects garbage.
	_, parErr := relay.UnmarshalFrame([]byte{0x00})
	if parErr == nil {
		t.Fatal("expected error for malformed frame, got nil")
	}
}

// ── Close Unblocks Receive ───────────────────────────────────────────────────
func TestBodyWSSBind_CloseUnblock(t *testing.T) {
	routeID := relay.RouteID(14)
	cred := "close-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 8)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	fns, _, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("BodyWSSBind.Open: %v", openErr)
	}

	// Close the bind.
	if clErr := bind.Close(); clErr != nil {
		t.Fatalf("BodyWSSBind.Close: %v", clErr)
	}

	// Receive should return ErrClosed.
	var (
		packets = make([][]byte, 1)
		sizes   = make([]int, 1)
		eps     = make([]conn.Endpoint, 1)
	)
	packets[0] = make([]byte, 4096)

	recvCh := make(chan recvResult, 1)
	go func() {
		count, batchErr := fns[0](packets, sizes, eps)
		recvCh <- recvResult{n: count, err: batchErr}
	}()

	select {
	case res := <-recvCh:
		if res.err == nil {
			t.Fatal("expected ErrClosed after Close, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for receive after Close")
	}
}

// ── Reconnect ────────────────────────────────────────────────────────────────
func TestBodyWSSBind_Reconnect(t *testing.T) {
	routeID := relay.RouteID(15)
	cred := "reconnect-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 8)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	_, _, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("first BodyWSSBind.Open: %v", openErr)
	}

	// Close and reconnect.
	if clErr := bind.Close(); clErr != nil {
		t.Fatalf("first BodyWSSBind.Close: %v", clErr)
	}
	_, _, openErr2 := bind.Open(0)
	if openErr2 != nil {
		t.Fatalf("reconnect BodyWSSBind.Open: %v", openErr2)
	}

	// Verify Send still works on the reconnected bind.
	payload := []byte{0x41, 0x42, 0x43}
	if sendErr := bind.Send([][]byte{payload}, &testEndpoint{}); sendErr != nil {
		t.Fatalf("reconnect Send: %v", sendErr)
	}

	// Check that the frame arrived at the relay.
	var frame []byte
	select {
	case f := <-captureCh:
		frame = f
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect frame")
	}
	parsed, parErr := relay.UnmarshalFrame(frame)
	if parErr != nil {
		t.Fatalf("UnmarshalFrame reconnect frame: %v", parErr)
	}
	assertPayloadEqual(t, parsed.Payload, payload)
}

// ── Race Safety ──────────────────────────────────────────────────────────────
func TestBodyWSSBind_RaceSafety(t *testing.T) {
	// Concurrent Send calls should not race against each other or Close.
	routeID := relay.RouteID(16)
	cred := "race-cred"
	injectCh := make(chan []byte, 1)
	captureCh := make(chan []byte, 16)

	srv := httptest.NewServer(fakeRelayHandler(t, routeID, injectCh, captureCh))
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")

	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    routeID,
		credential: cred,
	}
	_, _, openErr := bind.Open(0)
	if openErr != nil {
		t.Fatalf("BodyWSSBind.Open: %v", openErr)
	}

	// Spawn 3 goroutines that send concurrently.
	payloads := [][]byte{
		{0x01},
		{0x02},
		{0x03},
		{0x04},
		{0x05},
	}
	for i := range 5 {
		pl := payloads[i]
		go func() {
			bind.Send([][]byte{pl}, &testEndpoint{})
		}()
	}

	// Give goroutines time to finish.
	time.Sleep(2 * time.Second)

	// Close after concurrent sends — should not deadlock.
	if clErr := bind.Close(); clErr != nil {
		t.Fatalf("Close after concurrent sends: %v", clErr)
	}
}
