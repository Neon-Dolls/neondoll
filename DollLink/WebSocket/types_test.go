package ws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

// testLogger implements ws.Logger for tests.
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Info(msg string, fields ...map[string]any) {
	l.t.Logf("INFO: %s %v", msg, fields)
}
func (l *testLogger) Warn(msg string, fields ...map[string]any) {
	l.t.Logf("WARN: %s %v", msg, fields)
}
func (l *testLogger) Error(msg string, fields ...map[string]any) {
	l.t.Logf("ERROR: %s %v", msg, fields)
}

func TestNewWS(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)
	if srv == nil {
		t.Fatal("New returned nil")
	}
}

func TestWSEndpoint(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)

	// Plain HTTP request to /ws should fail upgrade (no WebSocket headers).
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	// Expect an upgrade error response (typically 200 with an error body for gorilla).
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
		t.Errorf("expected 200 or 400 from WS upgrade without headers, got %d", rec.Code)
	}
}

func TestWSStatus(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)

	req := httptest.NewRequest(http.MethodGet, "/ws/status", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected response body")
	}
}

func TestConnCount(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)
	if count := srv.ConnCount(); count != 0 {
		t.Errorf("expected 0 connections, got %d", count)
	}
}

func TestStartShutdown(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: "127.0.0.1:0"}, log, nil)

	go func() {
		srv.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SendAction failure boundary tests
// ---------------------------------------------------------------------------

func fakeConn(id string) *clientConn {
	return &clientConn{id: id, remote: "test"}
}

func TestSendAction_ZeroClients_ReturnsError(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)

	err := srv.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage,
		Payload: actions.SendMessagePayload{
			Text: "hello",
		},
	})
	if err == nil {
		t.Fatal("expected error for zero connected clients, got nil")
	}
}

func TestSendAction_AllWritesFail_ReturnsError(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)
	srv.writeJSONFn = func(cc *clientConn, event events.Event) error {
		return fmt.Errorf("mock write failure")
	}

	srv.mu.Lock()
	srv.conns["c1"] = fakeConn("c1")
	srv.conns["c2"] = fakeConn("c2")
	srv.mu.Unlock()

	err := srv.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage,
		Payload: actions.SendMessagePayload{
			Text: "hello",
		},
	})
	if err == nil {
		t.Fatal("expected error when all writes fail, got nil")
	}
}

func TestSendAction_PartialSuccess_ReturnsNil(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)

	var writeCount int
	srv.writeJSONFn = func(cc *clientConn, event events.Event) error {
		writeCount++
		// Client "c1" succeeds, client "c2" fails
		if cc.id == "c2" {
			return fmt.Errorf("mock write failure for c2")
		}
		return nil
	}

	srv.mu.Lock()
	srv.conns["c1"] = fakeConn("c1")
	srv.conns["c2"] = fakeConn("c2")
	srv.mu.Unlock()

	err := srv.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage,
		Payload: actions.SendMessagePayload{
			Text: "hello",
		},
	})
	if err != nil {
		t.Fatalf("expected nil for partial success, got: %v", err)
	}
	if writeCount != 2 {
		t.Fatalf("expected 2 write attempts, got %d", writeCount)
	}
}

func TestSendAction_AllWritesSucceed_ReturnsNil(t *testing.T) {
	log := &testLogger{t}
	srv := New(Config{Listen: ":0"}, log, nil)
	srv.writeJSONFn = func(cc *clientConn, event events.Event) error {
		return nil
	}

	srv.mu.Lock()
	srv.conns["c1"] = fakeConn("c1")
	srv.mu.Unlock()

	err := srv.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage,
		Payload: actions.SendMessagePayload{
			Text: "hello",
		},
	})
	if err != nil {
		t.Fatalf("expected nil when all writes succeed, got: %v", err)
	}
}
