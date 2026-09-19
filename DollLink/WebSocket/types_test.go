package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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