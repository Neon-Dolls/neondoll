package filetransfer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
)

// ── Test Helpers ─────────────────────────────────────────────────────────

// newTestWSConnDirect creates two WebSocket connections connected via a pipe
// in a more reliable way — both sides get upgraded from the same HTTP upgrade.
func newTestWSConnDirect(t *testing.T) (*WSConn, *WSConn, func()) {
	t.Helper()

	// Use a channel to pass the server-side connection.
	type result struct {
		conn *websocket.Conn
		err  error
	}
	ch := make(chan result, 1)

	var upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		ch <- result{conn, err}
	}))
	t.Cleanup(srv.Close)

	// Dial from client side.
	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	clientRaw, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Get the server-side connection.
	r := <-ch
	if r.err != nil {
		t.Fatalf("server upgrade: %v", r.err)
	}

	return NewWSConn(clientRaw), NewWSConn(r.conn), func() {
		clientRaw.Close()
		r.conn.Close()
	}
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestWSConnWriteReadControl(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	msg := &ControlMessage{Type: TypeOffer}

	if err := c1.WriteControl(msg); err != nil {
		t.Fatalf("write control: %v", err)
	}

	got, err := c2.ReadControl()
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if got.Type != TypeOffer {
		t.Fatalf("type: got %q, want %q", got.Type, TypeOffer)
	}
}

func TestWSConnWriteReadFrame(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	frame := NewNDF1Frame(tid, 0, []byte("hello frame"))

	if err := c1.WriteFrame(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	got, err := c2.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if got.Header.TransferID != tid {
		t.Fatalf("transfer_id mismatch")
	}
	if string(got.Payload) != "hello frame" {
		t.Fatalf("payload: got %q, want %q", string(got.Payload), "hello frame")
	}
}

func TestWSConnMixedTraffic(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	// Send control, then frame, then control.
	offer := &ControlMessage{
		Type: TypeOffer,
		Payload: Offer{
			FileID:     "file_abc",
			TransferID: tid,
			Size:       100,
			SHA256:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Name:       "test.bin",
			MediaType:  "application/octet-stream",
			Purpose:    "test",
		},
	}
	if err := c1.WriteControl(offer); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	frame := NewNDF1Frame(tid, 0, []byte("data chunk"))
	if err := c1.WriteFrame(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	done := &ControlMessage{
		Type: TypeComplete,
		Payload: Complete{
			FileID:     "file_abc",
			TransferID: tid,
		},
	}
	if err := c1.WriteControl(done); err != nil {
		t.Fatalf("write done: %v", err)
	}

	// Read in order.
	gotOffer, err := c2.ReadControl()
	if err != nil {
		t.Fatalf("read offer: %v", err)
	}
	if gotOffer.Type != TypeOffer {
		t.Fatalf("expected offer, got %q", gotOffer.Type)
	}

	gotFrame, err := c2.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if string(gotFrame.Payload) != "data chunk" {
		t.Fatalf("frame payload: got %q", gotFrame.Payload)
	}

	gotDone, err := c2.ReadControl()
	if err != nil {
		t.Fatalf("read done: %v", err)
	}
	if gotDone.Type != TypeComplete {
		t.Fatalf("expected complete, got %q", gotDone.Type)
	}
}

func TestWSConnReadWrongType(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	// Send a binary frame when expecting control.
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	frame := NewNDF1Frame(tid, 0, []byte("data"))
	if err := c1.WriteFrame(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	_, err := c2.ReadControl()
	if err == nil {
		t.Fatal("expected error reading binary as control")
	}
}

func TestWSConnReadAny(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	// Write a control message.
	msg := &ControlMessage{Type: TypeOffer}
	if err := c1.WriteControl(msg); err != nil {
		t.Fatalf("write control: %v", err)
	}

	val, err := c2.ReadAny()
	if err != nil {
		t.Fatalf("read any: %v", err)
	}
	if _, ok := val.(*ControlMessage); !ok {
		t.Fatalf("expected *ControlMessage, got %T", val)
	}

	// Write a frame.
	frame := NewNDF1Frame(tid, 0, []byte("payload"))
	if err := c1.WriteFrame(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	val, err = c2.ReadAny()
	if err != nil {
		t.Fatalf("read any frame: %v", err)
	}
	if _, ok := val.(*NDF1Frame); !ok {
		t.Fatalf("expected *NDF1Frame, got %T", val)
	}
}

func TestTransferSessionCreate(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	c1, _, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	session := NewTransferSession(c1, tid)
	if session.ID != tid {
		t.Fatalf("session ID mismatch")
	}
	if session.State() != StateNone {
		t.Fatalf("expected StateNone, got %v", session.State())
	}
}

func TestTransferSessionSend(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	session := NewTransferSession(c1, tid)

	msg := &ControlMessage{Type: TypeOffer}
	if err := session.SendControl(msg); err != nil {
		t.Fatalf("session send control: %v", err)
	}

	got, err := c2.ReadControl()
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if got.Type != TypeOffer {
		t.Fatalf("expected offer, got %q", got.Type)
	}

	frame := NewNDF1Frame(tid, 0, []byte("session frame"))
	if err := session.SendFrame(frame); err != nil {
		t.Fatalf("session send frame: %v", err)
	}

	gotFrame, err := c2.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if string(gotFrame.Payload) != "session frame" {
		t.Fatalf("payload mismatch")
	}
}

func TestWSConnWriteFrameMaxSize(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	// Write a frame with 256 KiB payload (recommended chunk size).
	payload := make([]byte, RecommendedChunkSize)
	for i := range payload {
		payload[i] = byte(i & 0xff)
	}
	frame := NewNDF1Frame(tid, 0, payload)
	if err := c1.WriteFrame(frame); err != nil {
		t.Fatalf("write 256KiB frame: %v", err)
	}

	got, err := c2.ReadFrame()
	if err != nil {
		t.Fatalf("read 256KiB frame: %v", err)
	}
	if len(got.Payload) != RecommendedChunkSize {
		t.Fatalf("payload length: got %d, want %d", len(got.Payload), RecommendedChunkSize)
	}
	if got.Header.Offset != 0 {
		t.Fatalf("offset: got %d, want 0", got.Header.Offset)
	}
}

func TestWSConnControlRoundTripFull(t *testing.T) {
	c1, c2, cleanup := newTestWSConnDirect(t)
	defer cleanup()

	tid := MustParseTransferID("e6b15a40-6a7b-4f8d-9c3e-12a5d8f7c0b1")

	offer := &ControlMessage{
		Type: TypeOffer,
		Payload: Offer{
			FileID:     "file_xyz",
			TransferID: tid,
			Size:       1048576,
			SHA256:     "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			Name:       "photo.jpg",
			MediaType:  "image/jpeg",
			Purpose:    "attachment",
		},
	}
	if err := c1.WriteControl(offer); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	got, err := c2.ReadControl()
	if err != nil {
		t.Fatalf("read offer: %v", err)
	}
	if got.Type != TypeOffer {
		t.Fatalf("type: got %q, want %q", got.Type, TypeOffer)
	}
	p, ok := got.Payload.(Offer)
	if !ok {
		t.Fatalf("payload type: got %T, want Offer", got.Payload)
	}
	if p.FileID != "file_xyz" {
		t.Fatalf("file_id: got %q, want %q", p.FileID, "file_xyz")
	}
	if p.TransferID != tid {
		t.Fatalf("transfer_id mismatch")
	}
	if p.Size != 1048576 {
		t.Fatalf("size: got %d, want %d", p.Size, 1048576)
	}
	if p.Name != "photo.jpg" {
		t.Fatalf("name: got %q, want %q", p.Name, "photo.jpg")
	}
}
