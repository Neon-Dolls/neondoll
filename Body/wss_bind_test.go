// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
	"golang.zx2c4.com/wireguard/conn"
)

const testCredential = "test-credential"

// ── test helpers ──────────────────────────────────────────────────────────────

// testEndpoint implements conn.Endpoint for Send calls.
type testEndpoint struct{}

func (testEndpoint) ClearSrc()           {}
func (testEndpoint) DstToString() string { return "" }
func (testEndpoint) DstToBytes() []byte  { return nil }
func (testEndpoint) DstIP() netip.Addr   { return netip.Addr{} }
func (testEndpoint) SrcToString() string { return "" }
func (testEndpoint) SrcToBytes() []byte  { return nil }
func (testEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// fakeRelayOpts drives per-connection behaviour of the fake relay.
type fakeRelayOpts struct {
	routeID relay.RouteID

	// rejectAuth reports whether the given connection (1-based ordinal)
	// should be refused auth (closed without a BodyAttached reply).
	rejectAuth func(ordinal int) bool

	// afterAttached runs immediately after BodyAttached is sent and reports
	// whether the connection should then be dropped (close + stop serving).
	// Useful to simulate an unexpected WSS loss to trigger client-side
	// automatic reconnect.
	afterAttached func(ordinal int, ws *websocket.Conn) bool
}

// fakeRelay is an in-process Relay /body WebSocket for tests.  It accepts the
// BodyAttach handshake, replies BodyAttached, forwards server→client frames
// from injectCh (only on the CURRENT live connection, never a stale one) and
// captures client→server frames into captureCh.
type fakeRelay struct {
	t         *testing.T
	opts      fakeRelayOpts
	mu        sync.Mutex
	connN     int
	curWS     *websocket.Conn
	injectCh  chan []byte
	captureCh chan []byte
}

func newFakeRelay(t *testing.T, opts fakeRelayOpts) *fakeRelay {
	t.Helper()
	return &fakeRelay{
		t:         t,
		opts:      opts,
		injectCh:  make(chan []byte, 256),
		captureCh: make(chan []byte, 512),
	}
}

// connections is the number of WebSocket connections accepted so far.
func (r *fakeRelay) connections() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connN
}

func (r *fakeRelay) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		up := websocket.Upgrader{}
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer ws.Close()

		r.mu.Lock()
		r.connN++
		ordinal := r.connN
		r.curWS = ws
		r.mu.Unlock()

		// Wait for the BodyAttach handshake.
		msgType, _, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.TextMessage {
			return
		}

		// Refuse auth: close without replying BodyAttached.
		if r.opts.rejectAuth != nil && r.opts.rejectAuth(ordinal) {
			return
		}

		resp, err := relay.MarshalControl(&relay.BodyAttached{
			Type:    relay.CmdBodyAttached,
			Version: relay.ProtocolVersion,
			RouteID: r.opts.routeID,
		})
		if err != nil {
			return
		}
		if err := ws.WriteMessage(websocket.TextMessage, resp); err != nil {
			return
		}

		if r.opts.afterAttached != nil && r.opts.afterAttached(ordinal, ws) {
			return
		}

		// Server→client injector: deliver frames only on the CURRENT live
		// connection, so a frame never reaches a stale websocket.
		go func(cws *websocket.Conn) {
			for frame := range r.injectCh {
				r.mu.Lock()
				live := r.curWS
				r.mu.Unlock()
				if live != cws {
					continue
				}
				if err := cws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					return
				}
			}
		}(ws)

		// Client→server capture loop.
		for {
			msgType, raw, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			select {
			case r.captureCh <- raw:
			default:
			}
		}
	}
}

func makeFrameBytes(t *testing.T, routeID relay.RouteID, payload []byte) []byte {
	t.Helper()
	frame, err := relay.MarshalFrame(&relay.Frame{
		Version: relay.ProtocolVersion,
		RouteID: routeID,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return frame
}

// openTestBind spins up a fake relay and opens a BodyWSSBind against it.
// tune (optional) configures the bind before Open (reconnect knobs, queue
// depth, credential).
func openTestBind(t *testing.T, r *fakeRelay, tune func(*BodyWSSBind)) (*BodyWSSBind, []conn.ReceiveFunc) {
	t.Helper()
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")
	bind := &BodyWSSBind{
		relayAddr:  addr,
		routeID:    r.opts.routeID,
		credential: testCredential,
	}
	if tune != nil {
		tune(bind)
	}

	fns, _, err := bind.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })
	return bind, fns
}

// bindWS returns the bind's currently installed websocket.
func bindWS(b *BodyWSSBind) *websocket.Conn {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	return b.ws
}

// waitFor polls cond until it returns true or timeout elapses.  Deterministic
// synchronization for async state (reconnects, goroutine completion) — never
// a fixed-duration sleep.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type recvResult struct {
	n    int
	data []byte
	err  error
}

// recvOne calls a receive func once and copies the delivered payload out.
func recvOne(fn conn.ReceiveFunc) recvResult {
	packets := make([][]byte, 1)
	sizes := make([]int, 1)
	eps := make([]conn.Endpoint, 1)
	packets[0] = make([]byte, 4096)
	n, err := fn(packets, sizes, eps)
	data := []byte(nil)
	if n > 0 && sizes[0] <= len(packets[0]) {
		data = append(data, packets[0][:sizes[0]]...)
	}
	return recvResult{n: n, data: data, err: err}
}

// receiveWithTimeout calls fn and waits up to timeout for a result; the
// non-nil error return means the call did not complete in time.
func receiveWithTimeout(fn conn.ReceiveFunc, timeout time.Duration) (recvResult, error) {
	ch := make(chan recvResult, 1)
	go func() { ch <- recvOne(fn) }()
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return recvResult{}, fmt.Errorf("receive timed out")
	}
}

// ── handshake / basic transport ───────────────────────────────────────────────

func TestBodyWSSBind_ConnectAttach(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, fns := openTestBind(t, r, nil)

	if len(fns) != 1 {
		t.Fatalf("want 1 receive func, got %d", len(fns))
	}
	if bindWS(bind) == nil {
		t.Fatal("bind has no live websocket after Open")
	}
	if got := r.connections(); got != 1 {
		t.Fatalf("want 1 accepted connection, got %d", got)
	}
}

func TestBodyWSSBind_SendPacket(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, _ := openTestBind(t, r, nil)

	payload := []byte("hello wg")
	if err := bind.Send([][]byte{payload}, testEndpoint{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case raw := <-r.captureCh:
		frame, err := relay.UnmarshalFrame(raw)
		if err != nil {
			t.Fatalf("unmarshal captured frame: %v", err)
		}
		if frame.RouteID != routeID {
			t.Fatalf("frame route %d, want %d", frame.RouteID, routeID)
		}
		if string(frame.Payload) != string(payload) {
			t.Fatalf("frame payload %q, want %q", frame.Payload, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame captured by relay")
	}
}

func TestBodyWSSBind_ReceivePacket(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	_, fns := openTestBind(t, r, nil)

	payload := []byte("from relay")
	r.injectCh <- makeFrameBytes(t, routeID, payload)

	res, err := receiveWithTimeout(fns[0], 2*time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if res.n != 1 || string(res.data) != string(payload) {
		t.Fatalf("received n=%d data=%q, want 1 %q", res.n, res.data, payload)
	}
}

func TestBodyWSSBind_FramingRoundTrip(t *testing.T) {
	routeID := relay.RouteID(42)
	payload := []byte("frame payload")
	frame, err := relay.MarshalFrame(&relay.Frame{
		Version: relay.ProtocolVersion,
		RouteID: routeID,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := relay.UnmarshalFrame(frame)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.RouteID != routeID {
		t.Fatalf("route %d, want %d", parsed.RouteID, routeID)
	}
	if string(parsed.Payload) != string(payload) {
		t.Fatalf("payload %q, want %q", parsed.Payload, payload)
	}
}

// ── failure paths ─────────────────────────────────────────────────────────────

func TestBodyWSSBind_FailedAuth(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{
		routeID:    routeID,
		rejectAuth: func(int) bool { return true },
	})
	srv := httptest.NewServer(r.handler())
	defer srv.Close()

	addr := "ws" + strings.TrimPrefix(srv.URL, "http")
	bind := &BodyWSSBind{
		relayAddr:        addr,
		routeID:          routeID,
		credential:       "wrong-credential",
		reconnectTries:   2,
		reconnectBackoff: 5 * time.Millisecond,
	}
	if _, _, err := bind.Open(0); err == nil {
		t.Fatal("Open should fail when auth is rejected")
	}
	// Bounded: exactly 2 attempts, no infinite retry.
	if got := r.connections(); got != 2 {
		t.Fatalf("want exactly 2 connection attempts, got %d", got)
	}
}

func TestBodyWSSBind_MalformedFrameDropped(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	_, fns := openTestBind(t, r, nil)

	// Too short to parse as a frame — must be dropped, not delivered.
	r.injectCh <- []byte{0x01, 0x02, 0x03}
	good := []byte("good frame")
	r.injectCh <- makeFrameBytes(t, routeID, good)

	res, err := receiveWithTimeout(fns[0], 2*time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if res.n != 1 || string(res.data) != string(good) {
		t.Fatalf("received n=%d data=%q, want only the good frame %q", res.n, res.data, good)
	}
}

func TestBodyWSSBind_WrongRouteFrameDropped(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	_, fns := openTestBind(t, r, nil)

	r.injectCh <- makeFrameBytes(t, relay.RouteID(999), []byte("wrong route"))
	good := []byte("right route")
	r.injectCh <- makeFrameBytes(t, routeID, good)

	res, err := receiveWithTimeout(fns[0], 2*time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if res.n != 1 || string(res.data) != string(good) {
		t.Fatalf("received n=%d data=%q, want only the right-route frame %q", res.n, res.data, good)
	}
}

func TestBodyWSSBind_QueueBounds(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, fns := openTestBind(t, r, func(b *BodyWSSBind) { b.queueDepth = 4 })

	// Inject 8 frames; with a 4-deep drop-new queue exactly 4 are delivered.
	for i := 0; i < 8; i++ {
		r.injectCh <- makeFrameBytes(t, routeID, []byte{byte(i)})
	}

	// Deterministic synchronization: wait until the read loop has consumed
	// all 8 frames (delivered or dropped on overflow) before consuming, so
	// the consumer cannot free slots that would let later frames in.
	waitFor(t, 3*time.Second, "read loop to process all 8 frames", func() bool {
		return bind.processed.Load() == 8
	})
	// With no consumer in flight, the queue holds exactly the first
	// queueDepth frames; the rest were dropped on overflow.
	if got, want := len(bind.inbox), 4; got != want {
		t.Fatalf("inbox holds %d frames after overflow, want %d", got, want)
	}

	delivered := map[byte]bool{}
	for i := 0; i < 4; i++ {
		res, err := receiveWithTimeout(fns[0], 2*time.Second)
		if err != nil {
			t.Fatalf("receive %d: %v", i, err)
		}
		if res.n != 1 || len(res.data) != 1 {
			t.Fatalf("receive %d: n=%d data=%v", i, res.n, res.data)
		}
		delivered[res.data[0]] = true
	}
	if len(delivered) != 4 {
		t.Fatalf("want 4 distinct delivered packets, got %v", delivered)
	}
	// The 5th receive must block forever (overflow was dropped) — bounded
	// negative assertion.
	if _, err := receiveWithTimeout(fns[0], 300*time.Millisecond); err == nil {
		t.Fatal("received a 5th packet; overflow must be dropped")
	}
}

// ── close semantics ───────────────────────────────────────────────────────────

func TestBodyWSSBind_SendAfterClose(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, _ := openTestBind(t, r, nil)

	if err := bind.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := bind.Send([][]byte{[]byte("late")}, testEndpoint{})
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Send after Close: %v, want net.ErrClosed", err)
	}
}

func TestBodyWSSBind_CloseIdempotent(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, _ := openTestBind(t, r, nil)

	if err := bind.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := bind.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestBodyWSSBind_CloseUnblocksReceive(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, fns := openTestBind(t, r, nil)

	resCh := make(chan recvResult, 1)
	go func() { resCh <- recvOne(fns[0]) }()

	if err := bind.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case res := <-resCh:
		if res.n != 0 {
			t.Fatalf("receive after Close returned n=%d", res.n)
		}
		if !errors.Is(res.err, net.ErrClosed) {
			t.Fatalf("receive after Close: %v, want net.ErrClosed", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not unblock after Close")
	}
}

// ── automatic reconnect ───────────────────────────────────────────────────────

func TestBodyWSSBind_TransientReconnect(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{
		routeID: routeID,
		afterAttached: func(ordinal int, ws *websocket.Conn) bool {
			if ordinal == 1 {
				_ = ws.Close() // drop the first transport unexpectedly
				return true
			}
			return false
		},
	})
	bind, fns := openTestBind(t, r, func(b *BodyWSSBind) {
		b.reconnectTries = 3
		b.reconnectBackoff = 5 * time.Millisecond
	})

	oldWS := bindWS(bind)
	if oldWS == nil {
		t.Fatal("bind has no websocket after Open")
	}

	// The bind must notice the loss and auto-reconnect to a NEW websocket —
	// no explicit Close/Open involved.
	waitFor(t, 3*time.Second, "automatic reconnect", func() bool {
		ws := bindWS(bind)
		return ws != nil && ws != oldWS
	})
	if got := r.connections(); got != 2 {
		t.Fatalf("want exactly 2 accepted connections (initial + reconnect), got %d", got)
	}

	// Transport resumes: Send works and lands on the relay.
	payload := []byte("post-reconnect-send")
	if err := bind.Send([][]byte{payload}, testEndpoint{}); err != nil {
		t.Fatalf("Send after reconnect: %v", err)
	}
	select {
	case raw := <-r.captureCh:
		frame, err := relay.UnmarshalFrame(raw)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(frame.Payload) != string(payload) {
			t.Fatalf("payload %q, want %q", frame.Payload, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame captured after reconnect")
	}

	// Transport resumes: Receive works.
	recvPayload := []byte("post-reconnect-recv")
	r.injectCh <- makeFrameBytes(t, routeID, recvPayload)
	res, err := receiveWithTimeout(fns[0], 2*time.Second)
	if err != nil {
		t.Fatalf("receive after reconnect: %v", err)
	}
	if res.n != 1 || string(res.data) != string(recvPayload) {
		t.Fatalf("received n=%d data=%q, want %q", res.n, res.data, recvPayload)
	}
}

func TestBodyWSSBind_ExhaustedReconnect(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{
		routeID: routeID,
		afterAttached: func(ordinal int, ws *websocket.Conn) bool {
			if ordinal == 1 {
				_ = ws.Close() // unexpected loss
				return true
			}
			return false
		},
		rejectAuth: func(ordinal int) bool { return ordinal >= 2 },
	})
	bind, fns := openTestBind(t, r, func(b *BodyWSSBind) {
		b.reconnectTries = 3
		b.reconnectBackoff = 5 * time.Millisecond
	})

	// Receive must fail/unblock cleanly once reconnection is exhausted.
	res, err := receiveWithTimeout(fns[0], 5*time.Second)
	if err != nil {
		t.Fatalf("Receive should unblock after reconnect exhaustion: %v", err)
	}
	if !errors.Is(res.err, ErrReconnectExhausted) {
		t.Fatalf("Receive error %v, want ErrReconnectExhausted", res.err)
	}

	// Send fails with the same terminal error.
	if err := bind.Send([][]byte{[]byte("late")}, testEndpoint{}); !errors.Is(err, ErrReconnectExhausted) {
		t.Fatalf("Send after exhaustion: %v, want ErrReconnectExhausted", err)
	}

	// Bounded: initial + exactly 3 reconnect attempts.
	if got := r.connections(); got != 4 {
		t.Fatalf("want exactly 4 accepted connections (initial + 3 attempts), got %d", got)
	}
}

func TestBodyWSSBind_StaleConnectionRejected(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{
		routeID: routeID,
		afterAttached: func(ordinal int, ws *websocket.Conn) bool {
			if ordinal == 1 {
				_ = ws.Close() // unexpected loss
				return true
			}
			return false
		},
	})
	bind, _ := openTestBind(t, r, func(b *BodyWSSBind) {
		b.reconnectTries = 3
		b.reconnectBackoff = 5 * time.Millisecond
	})

	oldWS := bindWS(bind)
	waitFor(t, 3*time.Second, "reconnect", func() bool {
		ws := bindWS(bind)
		return ws != nil && ws != oldWS
	})

	// The stale websocket must be dead: ReadMessage returns an error.
	staleReadCh := make(chan error, 1)
	go func() { _, _, err := oldWS.ReadMessage(); staleReadCh <- err }()
	select {
	case err := <-staleReadCh:
		if err == nil {
			t.Fatal("stale websocket still readable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale websocket ReadMessage did not return")
	}

	// The bind reconnected exactly once and now uses a NEW client-side
	// websocket (the server-side object for the same connection lives in
	// the relay; pointer equality across the wire is meaningless).  What
	// matters is that the stale server connection is gone and that sends
	// below land on the new connection (captured by conn2's handler, the
	// only live capture loop left).
	if got := r.connections(); got != 2 {
		t.Fatalf("relay saw %d connections, want 2 (initial + 1 reconnect)", got)
	}

	// Sends use the live transport, not the stale one.
	payload := []byte("live only")
	if err := bind.Send([][]byte{payload}, testEndpoint{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case raw := <-r.captureCh:
		frame, err := relay.UnmarshalFrame(raw)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(frame.Payload) != string(payload) {
			t.Fatalf("payload %q, want %q", frame.Payload, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame captured on live connection")
	}
}

// ── concurrency ───────────────────────────────────────────────────────────────

func TestBodyWSSBind_Concurrency(t *testing.T) {
	routeID := relay.RouteID(10)
	r := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	bind, fns := openTestBind(t, r, nil)

	const senders = 6
	const perSender = 8
	const totalFrames = senders * perSender

	// Deterministic start: all senders block until the barrier opens, then
	// run concurrently.  No sleeps involved.
	start := make(chan struct{})
	errCh := make(chan error, totalFrames)
	var wg sync.WaitGroup
	wg.Add(senders)
	for i := 0; i < senders; i++ {
		go func(seed byte) {
			defer wg.Done()
			<-start
			payload := []byte{seed}
			for j := 0; j < perSender; j++ {
				if err := bind.Send([][]byte{payload}, testEndpoint{}); err != nil {
					errCh <- fmt.Errorf("sender %d: %w", seed, err)
					return
				}
			}
		}(byte(0x10 + i))
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Send failed: %v", err)
	}

	// Deterministic drain: the relay must capture every frame.
	for i := 0; i < totalFrames; i++ {
		select {
		case <-r.captureCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("relay captured only %d/%d frames", i, totalFrames)
		}
	}
	if leftover := len(r.captureCh); leftover != 0 {
		t.Fatalf("unexpected extra captured frames: %d", leftover)
	}

	// A blocked Receive must be unblocked by concurrent Close with
	// net.ErrClosed — no stale packets after Close.
	recvCh := make(chan recvResult, 1)
	go func() { recvCh <- recvOne(fns[0]) }()

	if err := bind.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case res := <-recvCh:
		if res.n != 0 || !errors.Is(res.err, net.ErrClosed) {
			t.Fatalf("receive after Close: n=%d err=%v, want 0 / net.ErrClosed", res.n, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not unblock after Close")
	}

	// Idempotent under concurrency too.
	if err := bind.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
