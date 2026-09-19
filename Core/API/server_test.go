package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestHealthEndpoint(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", body["status"])
	}
}

func TestVersionEndpoint(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["version"] != float64(1) {
		t.Errorf("expected version 1, got %v", body["version"])
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestErrorResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	ErrorResponse(rec, http.StatusBadRequest, "bad input")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "bad input" {
		t.Errorf("expected 'bad input', got %v", body["error"])
	}
}

func TestParseBody(t *testing.T) {
	body := strings.NewReader(`{"key":"value"}`)
	req := httptest.NewRequest(http.MethodPost, "/test", body)

	var dest struct {
		Key string `json:"key"`
	}
	if err := ParseBody(req, &dest); err != nil {
		t.Fatalf("ParseBody: %v", err)
	}
	if dest.Key != "value" {
		t.Errorf("expected 'value', got %q", dest.Key)
	}
}

func TestParseBodyInvalidJSON(t *testing.T) {
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/test", body)

	var dest struct{}
	err := ParseBody(req, &dest)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var parseErr *ParseErr
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("unexpected error: %v", err)
	}
	_ = parseErr // use for type assertion check
}

func TestRegisterHandler(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: ":0"}, log)

	srv.RegisterHandler("/custom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte(`{"custom":true}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/custom", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("expected 418, got %d", rec.Code)
	}
}

func TestStartShutdown(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := New(Config{Listen: "127.0.0.1:0"}, log)

	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Start: %v", err)
		}
	}()

	// Give server time to start
	if err := srv.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}