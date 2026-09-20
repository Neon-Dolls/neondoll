//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/API"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// mockMindAPI implements dollmind.MindAPI for integration tests.
type mockMindAPI struct {
	s *dollstate.DollState
}

func (m *mockMindAPI) Inference() inference.Provider { return nil }
func (m *mockMindAPI) State() *dollstate.DollState   { return m.s }
func (m *mockMindAPI) Save() error                    { return nil }

func TestIntegrationStateSaveLoad(t *testing.T) {
	dir := t.TempDir()

	s := dollstate.NewDollState()
	s.Identity = dollstate.Identity{DollID: "integration-test", CanonicalName: "IT"}
	s.Soul = dollstate.Soul{Revision: 1, Content: "Integration test doll."}

	path, err := dollstate.SaveState(dir, &s)
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := dollstate.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if loaded.Identity.DollID != "integration-test" {
		t.Errorf("expected integration-test, got %s", loaded.Identity.DollID)
	}
}

func TestIntegrationConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neondoll.json")

	cfg := config.Defaults()
	cfg.Core.Profile = "integration"
	cfg.Core.Environment = "test"

	if err := config.Save(path, &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Core.Profile != "integration" {
		t.Errorf("expected profile integration, got %s", loaded.Core.Profile)
	}
}

func TestIntegrationAPIServer(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := api.New(api.Config{Listen: "127.0.0.1:0"}, log)

	// Start the server in the background.
	startErr := make(chan error, 1)
	go func() {
		startErr <- srv.Start()
	}()

	// Wait for the server to actually bind by polling Addr().
	addr := ""
	for range 50 {
		if a := srv.Addr(); a != "" {
			addr = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("server did not start within 500ms")
	}

	// Fail the test if Start() returned early.
	select {
	case err := <-startErr:
		t.Fatalf("Start returned unexpectedly: %v", err)
	default:
	}

	// Connection to the actual bound address must succeed.
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestIntegrationCognitionWithMock(t *testing.T) {
	provider := inference.NewMockProvider("integration-test", "Hello from integration!")
	log := logger.New(logger.InfoLevel, nil)
	mindAPI := &mockMindAPI{
		s: &dollstate.DollState{Version: dollstate.CurrentStateVersion},
	}
	sched := dollmind.New(provider, log, mindAPI)

	result, err := sched.Run(context.Background(), dollmind.LevelReflex, "test integration")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Level != dollmind.LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected at least one action")
	}
}

func TestIntegrationWSStartStop(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, nil)

	go func() {
		srv.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)

	if count := srv.ConnCount(); count != 0 {
		t.Errorf("expected 0 connections, got %d", count)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestIntegrationSecretRoundTrip(t *testing.T) {
	dir := t.TempDir()

	secrets := &dollstate.Secrets{
		Items: []dollstate.SecretItem{
			{Key: "api_key", Value: "«redacted:sk-…»", Source: "env"},
		},
	}

	path, err := dollstate.SaveSecrets(dir, secrets)
	if err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}

	loaded, err := dollstate.LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}

	if len(loaded.Items) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(loaded.Items))
	}
	if loaded.Items[0].Value != "«redacted:sk-…»" {
		t.Errorf("expected secret value, got %s", loaded.Items[0].Value)
	}
}

// decodeSparkFromCard decodes Spark's doll card. Kept in test helpers
// so the E2E test does not read the card during the interaction phase.
func decodeSparkFromCard(t *testing.T) *dollstate.DollState {
	t.Helper()
	cardPath := filepath.Join("..", "testdata", "dolls", "spark.dollcard")
	state, err := dollcard.Decode(cardPath)
	if err != nil {
		t.Fatalf("Decode(spark.dollcard): %v", err)
	}
	return state
}

// waitForWSAddr polls the WS server until it has a bound address.
func waitForWSAddr(t *testing.T, srv *ws.Server) string {
	t.Helper()
	for range 50 {
		if a := srv.Addr(); a != "" {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("WS server did not bind within 500ms")
	return ""
}

// wsConnect dials a WS server and returns the connection.
func wsConnect(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial("ws://"+addr+"/ws", http.Header{})
	if err != nil {
		t.Fatalf("WS dial: %v", err)
	}
	return conn
}

// sendAndReceive sends an event over WS and waits for a matching response.
func sendAndReceive(t *testing.T, conn *websocket.Conn, req events.Event, timeout time.Duration) *events.Event {
	t.Helper()
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("WS write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("WS read (waiting for correlation %s): %v", req.ID, err)
		}
		var resp events.Event
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Logf("skipping unparseable frame: %v", err)
			continue
		}
		if resp.CorrelationID == req.ID || resp.Type == events.TypeSystem {
			return &resp
		}
	}
}

// --- Spark through Doll Link: E2E tests ---

func TestIntegrationSparkThroughDollLink(t *testing.T) {
	// Phase 1: Seed the database from Spark's doll card (before Core starts).
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "neondoll.db")

	spark := decodeSparkFromCard(t)
	const sparkID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"
	const sparkName = "Spark"

	if spark.Identity.DollID != sparkID {
		t.Fatalf("Spark DollID = %q, want %q", spark.Identity.DollID, sparkID)
	}

	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveDoll(context.Background(), spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	// Phase 2: Start Core + Doll Link on a fresh store (must NOT read the doll card).
	log := logger.New(logger.WarnLevel, nil)

	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}
	defer store2.Close()

	// Use a mock provider for deterministic integration testing.
	mockProvider := inference.NewMockProvider("test", "Hello from Spark. I am here.")
	svc := interaction.New(store2, mockProvider, log)
	srv := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, svc)

	startErr := make(chan error, 1)
	go func() {
		startErr <- srv.Start(context.Background())
	}()

	addr := waitForWSAddr(t, srv)
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	select {
	case err := <-startErr:
		t.Fatalf("Start returned unexpectedly: %v", err)
	default:
	}

	// --- Subtest: successful interaction with persisted Spark ---
	t.Run("Spark responds", func(t *testing.T) {
		conn := wsConnect(t, addr)
		defer conn.Close()

		reqID := uuid.New().String()
		req := events.NewDollMessage(reqID, sparkID, "Hello Spark")

		resp := sendAndReceive(t, conn, req, 5*time.Second)
		if resp == nil {
			t.Fatal("no response received")
		}

		// Correlation must be preserved.
		if resp.CorrelationID != reqID {
			t.Errorf("CorrelationID = %q, want %q", resp.CorrelationID, reqID)
		}

		// Response must be a message type.
		if resp.Type != events.TypeMessage {
			t.Errorf("Type = %q, want %q", resp.Type, events.TypeMessage)
		}

		// Response must derive from loaded doll state.
		payload, ok := resp.Payload.(map[string]any)
		if !ok {
			// Try JSON re-marshal for typed payloads.
			b, _ := json.Marshal(resp.Payload)
			payload = make(map[string]any)
			json.Unmarshal(b, &payload)
		}

		text, _ := payload["text"].(string)
		if text == "" {
			t.Error("response text is empty")
		}

		// The response must contain the expected mock text.
		if !contains(text, "Spark") {
			t.Errorf("response text = %q, should contain %q", text, sparkName)
		}

		// Source should identify Core.
		if resp.Source != "core" {
			t.Errorf("Source = %q, want %q", resp.Source, "core")
		}

		// DollID echo.
		if resp.DollID != sparkID {
			t.Errorf("DollID = %q, want %q", resp.DollID, sparkID)
		}
	})

	// --- Subtest: unknown Doll ID returns clean error ---
	t.Run("Unknown doll returns error", func(t *testing.T) {
		conn := wsConnect(t, addr)
		defer conn.Close()

		reqID := uuid.New().String()
		req := events.NewDollMessage(reqID, "nonexistent-doll-id", "Hello?")

		resp := sendAndReceive(t, conn, req, 5*time.Second)
		if resp == nil {
			t.Fatal("no response received")
		}

		// Must be a system event (error).
		if resp.Type != events.TypeSystem {
			t.Errorf("Type = %q, want %q (system error)", resp.Type, events.TypeSystem)
		}

		// Correlation must still be preserved.
		if resp.CorrelationID != reqID {
			t.Errorf("CorrelationID = %q, want %q", resp.CorrelationID, reqID)
		}
	})

	// --- Subtest: Core stays alive after unknown doll request ---
	t.Run("Core alive after error", func(t *testing.T) {
		conn := wsConnect(t, addr)
		defer conn.Close()

		// Send an error-triggering request first.
		badReq := events.NewDollMessage(uuid.New().String(), "bad-id", "hi")
		_ = sendAndReceive(t, conn, badReq, 5*time.Second)

		// Now send a real request — must still work.
		goodReq := events.NewDollMessage(uuid.New().String(), sparkID, "are you still there?")
		resp := sendAndReceive(t, conn, goodReq, 5*time.Second)
		if resp == nil {
			t.Fatal("no response after error recovery")
		}
		if resp.Type == events.TypeSystem {
			payload, _ := resp.Payload.(map[string]any)
			t.Fatalf("got error after recovery: %v", payload)
		}
		if resp.DollID != sparkID {
			t.Errorf("response DollID = %q, want %q", resp.DollID, sparkID)
		}
	})

	// --- Subtest: Empty/invalid message shape ---
	t.Run("Invalid message rejected gracefully", func(t *testing.T) {
		conn := wsConnect(t, addr)
		defer conn.Close()

		// Send raw invalid JSON.
		if err := conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			// If the server closed the connection, that's acceptable
			// as long as the server is still running.
			t.Logf("connection closed after invalid message (acceptable): %v", err)
		} else {
			var resp events.Event
			if err := json.Unmarshal(raw, &resp); err == nil && resp.Type == events.TypeSystem {
				// Got a system error response — clean handling.
				t.Logf("invalid message produced system error as expected")
			}
		}

		// Verify server is still alive by connecting again.
		newConn := wsConnect(t, addr)
		defer newConn.Close()
		req := events.NewDollMessage(uuid.New().String(), sparkID, "still up?")
		if err := newConn.WriteJSON(req); err != nil {
			t.Fatalf("server dead after invalid message: %v", err)
		}
		newConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err = newConn.ReadMessage()
		if err != nil {
			t.Fatalf("server did not respond after invalid message: %v", err)
		}
	})
}

// contains is a helper for substring check.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}