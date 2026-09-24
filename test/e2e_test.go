//go:build e2e

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// findFreePort asks the kernel for an available TCP port.
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	defer l.Close()
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, fmt.Errorf("split host port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse port: %w", err)
	}
	return port, nil
}

// waitForLlamaServer polls the llama.cpp endpoint until it responds.
func waitForLlamaServer(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	endpoint := baseURL + "/health"
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("create health request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("llama.cpp server did not become ready within %v", timeout)
}

// readCmdOutput reads stderr lines from the llama-server process and
// returns the last non-empty line (which usually contains the bind message).
func readCmdOutput(rd *bufio.Reader, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	lastLine := ""
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			return lastLine, nil
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lastLine = trimmed
		}
	}
	return lastLine, nil
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

func TestE2E_SparkSpeaksRealInference(t *testing.T) {
	llamaServerPath := os.Getenv("NEONDOLL_LLAMA_SERVER")
	if llamaServerPath == "" {
		t.Skip("SKIP: NEONDOLL_LLAMA_SERVER not set — no llama.cpp server configured")
	}

	// Verify the llama-server binary exists and is executable.
	info, err := os.Stat(llamaServerPath)
	if err != nil {
		t.Fatalf("NEONDOLL_LLAMA_SERVER=%s: %v", llamaServerPath, err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("NEONDOLL_LLAMA_SERVER=%s is not executable", llamaServerPath)
	}

	// Locate the committed model GGUF.
	modelPath := filepath.Join("..", "testdata", "models", "spark-test-brain", "model.gguf")
	if _, err := os.Stat(modelPath); err != nil {
		t.Fatalf("model not found at %s: %v", modelPath, err)
	}

	// Find a free port.
	port, err := findFreePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Start llama-server.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, llamaServerPath,
		"-m", modelPath,
		"--port", fmt.Sprintf("%d", port),
		"--ctx-size", "512",
		"-ngl", "0",
		"-t", "2",
		"--no-webui",
	)
	// Capture stderr (llama.cpp logs there).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("llama-server start: %v", err)
	}

	// Ensure cleanup even if the test fails.
	defer func() {
		cancel()
		cmd.Wait() //nolint:errcheck
	}()

	// Wait for the server to become ready.
	if err := waitForLlamaServer(ctx, baseURL, 15*time.Second); err != nil {
		stderrLines, _ := readCmdOutput(bufio.NewReader(stderr), 500*time.Millisecond)
		t.Fatalf("llama-server did not start: %v\nlast stderr: %s", err, stderrLines)
	}

	t.Logf("llama.cpp server ready at %s", baseURL)

	// --- Now run the NeonDoll E2E stack ---

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "neondoll.db")

	// Decode Spark's doll card.
	sparkCardPath := filepath.Join("..", "testdata", "dolls", "spark.dollcard")
	spark, err := dollcard.Decode(sparkCardPath)
	if err != nil {
		t.Fatalf("Decode(spark.dollcard): %v", err)
	}
	const sparkID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"
	if spark.Identity.DollID != sparkID {
		t.Fatalf("Spark DollID = %q, want %q", spark.Identity.DollID, sparkID)
	}

	// Seed the database.
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveDoll(context.Background(), spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	store.Close()

	// Start Core + Doll Link on the seeded store.
	log := logger.New(logger.WarnLevel, nil)

	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}
	defer store2.Close()

	provider := inference.NewOpenAIProvider(
		inference.WithBaseURL(baseURL),
	)
	svc := interaction.New(store2, provider, log)

	wsServer := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, svc)

	startErr := make(chan error, 1)
	go func() {
		startErr <- wsServer.Start(context.Background())
	}()

	addr := waitForWSAddr(t, wsServer)
	defer func() {
		if err := wsServer.Shutdown(context.Background()); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	select {
	case err := <-startErr:
		t.Fatalf("Start returned unexpectedly: %v", err)
	default:
	}

	// --- Send a message through Doll Link addressed to Spark ---
	conn := wsConnect(t, addr)
	defer conn.Close()

	reqID := uuid.New().String()
	req := events.NewDollMessage(reqID, sparkID, "Hello Spark, tell me a story.")

	resp := sendAndReceive(t, conn, req, 30*time.Second)
	if resp == nil {
		t.Fatal("no response received")
	}

	// Assertion 13: CorrelationID is preserved.
	if resp.CorrelationID != reqID {
		t.Errorf("CorrelationID = %q, want %q", resp.CorrelationID, reqID)
	}

	// Assertion 14: Source identifies Core.
	if resp.Source != "core" {
		t.Errorf("Source = %q, want %q", resp.Source, "core")
	}

	// Assertion 15: DollID is correct.
	if resp.DollID != sparkID {
		t.Errorf("DollID = %q, want %q", resp.DollID, sparkID)
	}

	// Assertion 16: Response is a message (not error).
	if resp.Type != events.TypeMessage {
		t.Errorf("Type = %q, want %q (should be message, got error/system)", resp.Type, events.TypeMessage)
	}

	// Extract text payload.
	payload, ok := resp.Payload.(map[string]any)
	if !ok {
		b, _ := json.Marshal(resp.Payload)
		payload = make(map[string]any)
		json.Unmarshal(b, &payload)
	}
	text, _ := payload["text"].(string)
	if text == "" {
		t.Fatal("response text is empty — no tokens generated")
	}

	t.Logf("Spark says: %q", text)

	// Assertion 17: Generated text is non-empty (proved above).
	// Assertion 18: Text should contain something plausible (at minimum non-whitespace).
	if strings.TrimSpace(text) == "" {
		t.Error("response text is only whitespace")
	}

	// Assertion 19: No deterministic/mock/fallback provider — if the real
	// provider was used, the response will be TinyStories-style prose, not
	// "Hello. I am Spark." or similar hardcoded text.
	if strings.HasPrefix(text, "Hello. I am") {
		t.Errorf("response appears to be the old deterministic fallback: %q", text)
	}

	// --- Assertion 20: Core remains healthy after the response ---
	conn2 := wsConnect(t, addr)
	defer conn2.Close()
	reqID2 := uuid.New().String()
	req2 := events.NewDollMessage(reqID2, sparkID, "are you still there?")
	resp2 := sendAndReceive(t, conn2, req2, 10*time.Second)
	if resp2 == nil {
		t.Fatal("no response after first interaction")
	}
	if resp2.Type == events.TypeSystem {
		t.Fatal("Core returned error after successful inference")
	}
	t.Log("E2E real inference test PASSED — Spark speaks!")
}

// decodeSparkFromCard decodes Spark's doll card for E2E tests.
func decodeSparkFromCard(t *testing.T) *dollstate.DollState {
	t.Helper()
	cardPath := filepath.Join("..", "testdata", "dolls", "spark.dollcard")
	state, err := dollcard.Decode(cardPath)
	if err != nil {
		t.Fatalf("Decode(spark.dollcard): %v", err)
	}
	return state
}

func TestE2E_InferenceFails(t *testing.T) {
	// This test verifies that when the inference endpoint is unreachable,
	// Core returns a clean error and remains alive.

	// Phase 1: Seed the database.
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "neondoll.db")

	spark := decodeSparkFromCard(t)
	const sparkID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"

	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveDoll(context.Background(), spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	store.Close()

	// Phase 2: Start Core with a provider pointing to a non-existent server.
	log := logger.New(logger.WarnLevel, nil)
	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store2.Close()

	// Use a provider with a bad base URL (nothing listening on that port).
	badProvider := inference.NewOpenAIProvider(
		inference.WithBaseURL("http://127.0.0.1:15999"),
	)
	svc := interaction.New(store2, badProvider, log)
	wsServer := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, svc)

	startErr := make(chan error, 1)
	go func() {
		startErr <- wsServer.Start(context.Background())
	}()

	addr := waitForWSAddr(t, wsServer)
	defer func() {
		if err := wsServer.Shutdown(context.Background()); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	select {
	case err := <-startErr:
		t.Fatalf("Start returned unexpectedly: %v", err)
	default:
	}

	// Send a message — should fail inference but return a clean error.
	conn := wsConnect(t, addr)
	defer conn.Close()

	reqID := uuid.New().String()
	req := events.NewDollMessage(reqID, sparkID, "Hello Spark")
	resp := sendAndReceive(t, conn, req, 10*time.Second)
	if resp == nil {
		t.Fatal("no response received")
	}

	// Must be a system (error) event.
	if resp.Type != events.TypeSystem {
		t.Errorf("expected system (error) event, got %v", resp.Type)
	}

	// CorrelationID must be preserved.
	if resp.CorrelationID != reqID {
		t.Errorf("CorrelationID = %q, want %q", resp.CorrelationID, reqID)
	}

	t.Log("Inference failure returned clean error")

	// Send another request on a new connection — Core must still process it
	// after the first provider failure.
	conn2 := wsConnect(t, addr)
	defer conn2.Close()
	reqID2 := uuid.New().String()
	req2 := events.NewDollMessage(reqID2, sparkID, "still up?")
	resp2 := sendAndReceive(t, conn2, req2, 10*time.Second)
	if resp2 == nil {
		t.Fatal("no response after first inference failure")
	}

	if resp2.Type != events.TypeSystem {
		t.Errorf("second response: expected system (error) event, got %v", resp2.Type)
	}
	if resp2.CorrelationID != reqID2 {
		t.Errorf("second response: CorrelationID = %q, want %q", resp2.CorrelationID, reqID2)
	}

	t.Log("Core remained operational after inference failure — second request also returned clean error")
}
