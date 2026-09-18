package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Logger"
)

func TestNewWS(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)
	if srv == nil {
		t.Fatal("New returned nil")
	}
}

func TestWSEndpoint(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected response body")
	}
}

func TestWSStatus(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

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
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)
	if count := srv.ConnCount(); count != 0 {
		t.Errorf("expected 0 connections, got %d", count)
	}
}

func TestStartShutdown(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: "127.0.0.1:0"}, log)

	go func() {
		srv.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}