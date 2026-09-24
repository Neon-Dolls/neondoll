//go:build e2e

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	ft "github.com/Neon-Dolls/neondoll/DollLink/FileTransfer"
)

// ── Test peer ───────────────────────────────────────────────────────────

// e2eTestServer is a small WebSocket server that implements the file transfer
// protocol on the receiving side.
type e2eTestServer struct {
	t       *testing.T
	srv     *http.Server
	addr    string
	storage string // temp dir for partial files
}

func newE2ETestServer(t *testing.T) *e2eTestServer {
	storage := t.TempDir()

	s := &e2eTestServer{
		t:       t,
		storage: storage,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = listener.Addr().String()

	s.srv = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := s.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Logf("server error: %v", err)
		}
	}()

	return s
}

func (s *e2eTestServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

func (s *e2eTestServer) url() string {
	return "ws://" + s.addr + "/ws"
}

// handleWS accepts one WebSocket connection and acts as a receiver.
// It understands the file transfer control messages and NDF1 binary frames.
// Uses OpenRetained so that a reconnection with the same file_id finds
// the partial file and resumes at the correct offset.
func (s *e2eTestServer) handleWS(w http.ResponseWriter, r *http.Request) {
	// Route file storage to the test server's temp directory so
	// completed files appear where the test can find them.
	ft.DefaultStorageDir = s.storage
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.t.Logf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	var rs *ft.ReceiveState
	var offer *ft.Offer

	for {
		mt, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		switch mt {
		case websocket.TextMessage:
			// ── JSON control message ────────────────────────────────────
			var msg ft.ControlMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				s.t.Logf("bad JSON: %v", err)
				continue
			}

			switch p := msg.Payload.(type) {
			case ft.Offer:
				s.t.Logf("server received offer: file=%s size=%d", p.FileID, p.Size)
				// Accept at offset 0 (new transfer)
				offer = &p
				rs, err = ft.OpenRetained(p.FileID, p.TransferID, p.Size, p.SHA256)
				if err != nil {
					s.t.Logf("NewReceiveState: %v", err)
					reject := ft.ControlMessage{Type: ft.TypeReject, Payload: ft.Reject{FileID: p.FileID, TransferID: p.TransferID, Reason: "io_error", Message: err.Error()}}
					b, _ := json.Marshal(reject)
					_ = conn.WriteMessage(websocket.TextMessage, b)
					return
				}
				// Send accept with the actual retained offset
				accept := ft.ControlMessage{Type: ft.TypeAccept, Payload: ft.Accept{
					FileID:     p.FileID,
					TransferID: p.TransferID,
					Offset:     rs.RetainedOffset(),
				}}
				b, _ := json.Marshal(accept)
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					s.t.Logf("write accept: %v", err)
					return
				}

			case ft.Accept:
				// server received accept (reversed direction, not expected here)
				s.t.Logf("server ignoring accept")

			case ft.Reject:
				s.t.Logf("server received reject: %s", p.Reason)
				return

			case ft.Complete:
				if rs == nil {
					s.t.Logf("complete with no receive state")
					return
				}
				if rs.RetainedOffset() >= rs.Size {
					if !rs.IsCompleted() {
						_ = rs.Complete()
					}
					recv := ft.ControlMessage{Type: ft.TypeReceived, Payload: ft.Received{
						FileID:     offer.FileID,
						TransferID: offer.TransferID,
					}}
					b, _ := json.Marshal(recv)
					_ = conn.WriteMessage(websocket.TextMessage, b)
				} else {
					s.t.Logf("complete but file not finished (retained=%d, need %d)", rs.RetainedOffset(), rs.Size)
				}

			case ft.Received:
				s.t.Logf("server received file.received (direction not expected)")
				return

			case ft.Cancel:
				s.t.Logf("server received cancel: %s", p.Reason)
				return
			}

		case websocket.BinaryMessage:
			// ── NDF1 binary frame ──────────────────────────────────────
			if rs == nil {
				s.t.Logf("binary frame with no receive state")
				return
			}
			frame, err := ft.DecodeNDF1Frame(bytes.NewReader(raw))
			if err != nil {
				s.t.Logf("decode NDF1: %v", err)
				return
			}
			if err := rs.WriteFrame(frame); err != nil {
				s.t.Logf("WriteFrame: %v", err)
				return
			}
			if rs.RetainedOffset() >= rs.Size {
				if err := rs.Complete(); err != nil {
					s.t.Logf("Complete error: %v", err)
				} else {
					s.t.Logf("file complete via binary frames (%d bytes)", rs.RetainedOffset())
				}
			}
		}
	}
}

// ── Helper: send a file over WebSocket (sender side) ───────────────────

// sendFileToPeer connects to the test server, offers a file, streams it in
// NDF1 frames, and waits for file.received or error.
func sendFileToPeer(t *testing.T, url string, srcPath string, fid ft.FileID, sha string) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	sz := int64(fileSize(t, srcPath))
	tid := ft.TransferIDFromUUID(uuid.New())

	// ── Offer ───────────────────────────────────────────────────────
	offer := ft.ControlMessage{Type: ft.TypeOffer, Payload: ft.Offer{
		FileID:     fid,
		TransferID: tid,
		Size:       sz,
		SHA256:     sha,
		Metadata:   map[string]string{"name": filepath.Base(srcPath)},
	}}
	b, _ := json.Marshal(offer)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	// ── Wait for accept ────────────────────────────────────────────
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	var msg ft.ControlMessage
	if err := json.Unmarshal(resp, &msg); err != nil {
		t.Fatalf("parse accept: %v", err)
	}
	if msg.Type != ft.TypeAccept {
		t.Fatalf("expected accept, got %s", msg.Type)
	}

	// ── Stream NDF1 frames ─────────────────────────────────────────
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	buf := make([]byte, ft.RecommendedChunkSize)
	var offset int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			frame := ft.NewNDF1Frame(tid, uint64(offset), buf[:n])
			wc, err := conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				t.Fatalf("next writer: %v", err)
			}
			if _, err := frame.Encode(wc); err != nil {
				wc.Close()
				t.Fatalf("encode frame: %v", err)
			}
			wc.Close()
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	// ── Complete ───────────────────────────────────────────────────
	comp := ft.ControlMessage{Type: ft.TypeComplete, Payload: ft.Complete{
		FileID:     fid,
		TransferID: tid,
	}}
	b, _ = json.Marshal(comp)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write complete: %v", err)
	}

	// ── Wait for received ──────────────────────────────────────────
	_, resp, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read received: %v", err)
	}
	if err := json.Unmarshal(resp, &msg); err != nil {
		t.Fatalf("parse received: %v", err)
	}
	if msg.Type != ft.TypeReceived {
		t.Fatalf("expected received, got %s", msg.Type)
	}
	t.Log("sender: file.received confirmed")
}

// helper for sendFileToPeer above
func fileSize(t *testing.T, path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// ── Test: Core → Body, 20 MiB file ───────────────────────────────────

func TestM5_FileTransfer_CoreToBody(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 20 MiB e2e test in short mode")
	}
	server := newE2ETestServer(t)
	defer server.close()

	// Generate 20 MiB random file
	size := 20 * 1024 * 1024
	srcPath := filepath.Join(t.TempDir(), "core_to_body.bin")
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	sha := ft.SHA256Hex(data)

	fid := ft.FileID("test:core-to-body-20mib")
	sendFileToPeer(t, server.url(), srcPath, fid, sha)

	// Verify the server received the file correctly by checking
	// the partial file in storage.
	files, err := os.ReadDir(filepath.Join(server.storage, "neondoll-transfer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files in server storage")
	}
	// Find the completed file — skip .partial files
	var matched bool
	for _, fi := range files {
		if fi.IsDir() || strings.HasSuffix(fi.Name(), ".partial") {
			continue
		}
		matched = true
		recvPath := filepath.Join(server.storage, "neondoll-transfer", fi.Name())
		recvData, err := os.ReadFile(recvPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(recvData) != size {
			t.Fatalf("size mismatch: got %d, want %d", len(recvData), size)
		}
		if !bytes.Equal(recvData, data) {
			t.Fatal("content mismatch")
		}
		recvSHA := ft.SHA256Hex(recvData)
		if recvSHA != sha {
			t.Fatalf("SHA mismatch: got %s, want %s", recvSHA, sha)
		}
		t.Logf("Core→Body: %d bytes verified (%s)", len(recvData), recvSHA)
	}
	if !matched {
		t.Fatal("no completed file found in server storage")
	}
}

// ── Test: Body → Core, 20 MiB file with resume ─────────────────────────
//
// Tests the Body→Core direction: the "Body" (test peer acting as sender)
// connects to the "Core" (server acting as receiver) and sends a 20 MiB
// file. The WebSocket connection is forcibly broken mid-transfer, then
// the Body reconnects and resumes from the retained offset. The final
// file is verified by size, SHA-256, and byte equality.

func TestM5_FileTransfer_BodyToCore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 20 MiB e2e test in short mode")
	}
	server := newE2ETestServer(t)
	defer server.close()

	// Generate 20 MiB file that the "Body" will send to "Core"
	size := 20 * 1024 * 1024
	srcPath := filepath.Join(t.TempDir(), "body_to_core.bin")
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	sha := ft.SHA256Hex(data)
	fid := ft.FileID("test:body-to-core-20mib")

	// 1. First connection: offer, send ~200 KiB, then kill the connection
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(server.url(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	tid := ft.TransferIDFromUUID(uuid.New())
	chunkSz := 256 * 1024
	firstBatch := int64(200 * 1024) // send ~200 KiB before killing

	// Build the sender state for the full file
	sender, err := ft.NewSenderState(fid, tid, srcPath, int64(size), chunkSz)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer sender.Close()

	// Offer the file
	offer := ft.ControlMessage{
		Type: ft.TypeOffer,
		Payload: ft.Offer{
			FileID:     fid,
			TransferID: tid,
			Size:       int64(size),
			SHA256:     sha,
			Metadata:   map[string]string{"name": "body_to_core.bin"},
		},
	}
	b, _ := json.Marshal(offer)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	// Wait for accept (offset 0)
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	var msg ft.ControlMessage
	if err := json.Unmarshal(resp, &msg); err != nil {
		t.Fatalf("parse accept: %v", err)
	}
	if msg.Type != ft.TypeAccept {
		t.Fatalf("expected accept, got %s", msg.Type)
	}
	accept, ok := msg.Payload.(ft.Accept)
	if !ok {
		t.Fatalf("payload type: %T", msg.Payload)
	}
	if accept.Offset != 0 {
		t.Fatalf("first accept offset: got %d, want 0", accept.Offset)
	}
	t.Logf("Body→Core 1st offer accepted @ offset %d", accept.Offset)

	// Send ~200 KiB in chunks, then kill the connection
	var sent int64
	for sent < firstBatch {
		frame, fErr := sender.NextFrame()
		if fErr == io.EOF {
			break
		}
		if fErr != nil {
			t.Fatalf("NextFrame: %v", fErr)
		}
		frameBytes, mErr := frame.MarshalBinary()
		if mErr != nil {
			t.Fatalf("marshal frame @ offset %d: %v", frame.Header.Offset, mErr)
		}
		if wErr := conn.WriteMessage(websocket.BinaryMessage, frameBytes); wErr != nil {
			t.Fatalf("write frame @ offset %d: %v", frame.Header.Offset, wErr)
		}
		sent += int64(len(frame.Payload))
	}

	// 2. Shoot the connection before the file is complete
	t.Logf("killing connection after %d bytes...", sent)
	conn.Close()

	// Allow server to process the last frames
	time.Sleep(200 * time.Millisecond)

	// 3. Reconnect with same file_id, new transfer_id, resume
	conn2, _, err := dialer.Dial(server.url(), nil)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer conn2.Close()

	tid2 := ft.TransferIDFromUUID(uuid.New())
	offer2 := ft.ControlMessage{
		Type: ft.TypeOffer,
		Payload: ft.Offer{
			FileID:     fid,
			TransferID: tid2,
			Size:       int64(size),
			SHA256:     sha,
			Metadata:   map[string]string{"name": "body_to_core.bin"},
		},
	}
	b2, _ := json.Marshal(offer2)
	if err := conn2.WriteMessage(websocket.TextMessage, b2); err != nil {
		t.Fatalf("write re-offer: %v", err)
	}

	// 4. Wait for re-accept with non-zero offset
	_, resp2, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read re-accept: %v", err)
	}
	var msg2 ft.ControlMessage
	if err := json.Unmarshal(resp2, &msg2); err != nil {
		t.Fatalf("parse re-accept: %v", err)
	}
	if msg2.Type != ft.TypeAccept {
		t.Fatalf("expected re-accept, got %s", msg2.Type)
	}
	accept2, ok := msg2.Payload.(ft.Accept)
	if !ok {
		t.Fatalf("payload2 type: %T", msg2.Payload)
	}
	if accept2.Offset == 0 {
		t.Fatal("re-accept offset is 0 — resume did not work")
	}
	t.Logf("Body→Core re-accepted @ offset %d (resume confirmed)", accept2.Offset)

	// 5. Seek sender to the resume offset and send the rest
	if err := sender.SeekTo(accept2.Offset); err != nil {
		t.Fatalf("SeekTo(%d): %v", accept2.Offset, err)
	}
	for {
		frame, fErr := sender.NextFrame()
		if fErr == io.EOF {
			break
		}
		if fErr != nil {
			t.Fatalf("NextFrame after resume: %v", fErr)
		}
		frameBytes, mErr := frame.MarshalBinary()
		if mErr != nil {
			t.Fatalf("marshal frame @ offset %d: %v", frame.Header.Offset, mErr)
		}
		if wErr := conn2.WriteMessage(websocket.BinaryMessage, frameBytes); wErr != nil {
			t.Fatalf("write frame @ offset %d: %v", frame.Header.Offset, wErr)
		}
	}

	// 6. Send file.complete
	comp := ft.ControlMessage{
		Type: ft.TypeComplete,
		Payload: ft.Complete{
			FileID:     fid,
			TransferID: tid2,
		},
	}
	b4, _ := json.Marshal(comp)
	if err := conn2.WriteMessage(websocket.TextMessage, b4); err != nil {
		t.Fatalf("write complete: %v", err)
	}

	// 7. Wait for file.received
	_, resp3, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read received: %v", err)
	}
	var msg3 ft.ControlMessage
	if err := json.Unmarshal(resp3, &msg3); err != nil {
		t.Fatalf("parse received: %v", err)
	}
	if msg3.Type != ft.TypeReceived {
		t.Fatalf("expected received, got %s", msg3.Type)
	}

	// 7. Verify the completed file on the "Core" receiver
	files, err := os.ReadDir(filepath.Join(server.storage, "neondoll-transfer"))
	if err != nil {
		t.Fatal(err)
	}
	var verified bool
	for _, fi := range files {
		if fi.IsDir() || strings.HasSuffix(fi.Name(), ".partial") {
			continue
		}
		recvPath := filepath.Join(server.storage, "neondoll-transfer", fi.Name())
		recvData, err := os.ReadFile(recvPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(recvData) != size {
			t.Fatalf("Body→Core size mismatch: %d vs %d", len(recvData), size)
		}
		if !bytes.Equal(recvData, data) {
			t.Fatal("Body→Core content mismatch")
		}
		recvSHA := ft.SHA256Hex(recvData)
		if recvSHA != sha {
			t.Fatalf("Body→Core SHA mismatch: %s vs %s", recvSHA, sha)
		}
		t.Logf("Body→Core verified: %d bytes, SHA=%s", len(recvData), recvSHA)
		verified = true
	}
	if !verified {
		t.Fatal("Body→Core: no completed file found in Core storage")
	}

	t.Logf("Body→Core resume test passed: %d/%d bytes after resume @ offset %d",
		accept2.Offset, size, accept2.Offset)
}

// ── Test: Resume after interrupted transfer ──────────────────────────

func TestM5_FileTransfer_Resume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 20 MiB e2e test in short mode")
	}
	server := newE2ETestServer(t)
	defer server.close()

	// Generate 20 MiB file
	size := 20 * 1024 * 1024
	srcPath := filepath.Join(t.TempDir(), "resume_source.bin")
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	sha := ft.SHA256Hex(data)
	fid := ft.FileID("test:resume-20mib")

	// First transfer — will kill mid-stream.
	// Use a dedicated server that allows us to control the connection.
	// For the resume test, we need:
	// 1. Dial, offer, receive accept
	// 2. Stream a chunk, then forcibly close (simulating transport loss)
	// 3. Retain the partial file on the server
	// 4. Reconnect with same file_id, new transfer_id, get accept with non-zero offset
	// 5. Stream remaining bytes
	// 6. Complete, verify SHA

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(server.url(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	tid1 := ft.TransferIDFromUUID(uuid.New())

	// Offer
	offer := ft.ControlMessage{Type: ft.TypeOffer, Payload: ft.Offer{
		FileID: fid, TransferID: tid1, Size: int64(size), SHA256: sha,
	}}
	b, _ := json.Marshal(offer)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	// Read accept
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	var msg ft.ControlMessage
	if err := json.Unmarshal(resp, &msg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Type != ft.TypeAccept {
		t.Fatalf("expected accept, got %s", msg.Type)
	}
	accept := msg.Payload.(ft.Accept)
	if accept.Offset != 0 {
		t.Fatalf("expected offset 0, got %d", accept.Offset)
	}

	// Stream only ~200 KiB then kill
	killAfter := 200 * 1024
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, ft.RecommendedChunkSize)
	var sent int64
	for sent < int64(killAfter) {
		n, err := f.Read(buf)
		if n > 0 {
			frame := ft.NewNDF1Frame(tid1, uint64(sent), buf[:n])
			wc, err := conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				break
			}
			_, _ = frame.Encode(wc)
			wc.Close()
			sent += int64(n)
		}
		if err != nil {
			break
		}
	}
	f.Close()
	// Forcibly close the connection
	conn.Close()
	t.Logf("First transfer killed after %d bytes sent", sent)

	// Allow server to process
	time.Sleep(200 * time.Millisecond)

	// ── Resume: reconnect, offer same file_id, new transfer_id ────
	conn2, _, err := dialer.Dial(server.url(), nil)
	if err != nil {
		t.Fatalf("re-dial: %v", err)
	}
	defer conn2.Close()

	tid2 := ft.TransferIDFromUUID(uuid.New())

	// Re-offer with same file_id
	offer2 := ft.ControlMessage{Type: ft.TypeOffer, Payload: ft.Offer{
		FileID: fid, TransferID: tid2, Size: int64(size), SHA256: sha,
	}}
	b2, _ := json.Marshal(offer2)
	if err := conn2.WriteMessage(websocket.TextMessage, b2); err != nil {
		t.Fatalf("write re-offer: %v", err)
	}

	// Read accept (should have non-zero offset if resume worked)
	_, resp2, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read re-accept: %v", err)
	}
	if err := json.Unmarshal(resp2, &msg); err != nil {
		t.Fatalf("parse re-accept: %v", err)
	}
	if msg.Type != ft.TypeAccept {
		t.Fatalf("expected accept on resume, got %s", msg.Type)
	}
	accept2 := msg.Payload.(ft.Accept)
	t.Logf("Resume accept offset: %d", accept2.Offset)
	if accept2.Offset == 0 {
		t.Fatal("resume failed: offset is 0, expected > 0")
	}

	// Resume streaming from the server's retained offset
	f2, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if _, err := f2.Seek(accept2.Offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	buf2 := make([]byte, ft.RecommendedChunkSize)
	var resumedOffset int64 = accept2.Offset
	for {
		n, err := f2.Read(buf2)
		if n > 0 {
			frame := ft.NewNDF1Frame(tid2, uint64(resumedOffset), buf2[:n])
			wc, err := conn2.NextWriter(websocket.BinaryMessage)
			if err != nil {
				t.Fatalf("resume writer: %v", err)
			}
			_, _ = frame.Encode(wc)
			wc.Close()
			resumedOffset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("resume read: %v", err)
		}
	}

	// Complete
	comp := ft.ControlMessage{Type: ft.TypeComplete, Payload: ft.Complete{
		FileID: fid, TransferID: tid2,
	}}
	b3, _ := json.Marshal(comp)
	if err := conn2.WriteMessage(websocket.TextMessage, b3); err != nil {
		t.Fatalf("write complete: %v", err)
	}

	// Wait for received
	_, resp3, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read received: %v", err)
	}
	if err := json.Unmarshal(resp3, &msg); err != nil {
		t.Fatalf("parse received: %v", err)
	}
	if msg.Type != ft.TypeReceived {
		t.Fatalf("expected received, got %s", msg.Type)
	}

	// Verify the completed file
	files, err := os.ReadDir(filepath.Join(server.storage, "neondoll-transfer"))
	if err != nil {
		t.Fatal(err)
	}
	var verified bool
	for _, fi := range files {
		if fi.IsDir() || strings.HasSuffix(fi.Name(), ".partial") {
			continue
		}
		recvPath := filepath.Join(server.storage, "neondoll-transfer", fi.Name())
		recvData, err := os.ReadFile(recvPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(recvData) != size {
			t.Fatalf("resume size mismatch: %d vs %d", len(recvData), size)
		}
		if !bytes.Equal(recvData, data) {
			t.Fatal("resume content mismatch")
		}
		recvSHA := ft.SHA256Hex(recvData)
		if recvSHA != sha {
			t.Fatalf("resume SHA mismatch: %s vs %s", recvSHA, sha)
		}
		t.Logf("Resume verified: %d bytes, SHA=%s", len(recvData), recvSHA)
		verified = true
	}
	if !verified {
		t.Fatal("no completed file found after resume")
	}

	t.Logf("Resume test passed: %d/%d bytes after resume @ offset %d",
		accept2.Offset, size, accept2.Offset)
}
