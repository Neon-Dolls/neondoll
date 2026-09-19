//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// TestContinuity_MemorySurvivesStoreReopen proves that Memory persisted during
// a full Doll Link interaction survives a Store close-and-reopen cycle.
//
// Phase 1 — Seed the database from Spark's Doll Card (before any server starts).
// Phase 2 — Start a WS server backed by Interaction + mock provider;
//
//	connect over WS, send a message, verify the response arrived
//	through Doll Link, then shut down the server cleanly.
//
// Phase 3 — Close the store at the restart boundary, open a fresh store
// against the same DB, load Spark, and verify the exact semantic Memory
// records remain without re-reading the doll card.
func TestContinuity_MemorySurvivesStoreReopen(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "neondoll.db")

	// ---- Phase 1: Seed the database from Spark's Doll Card ----
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
	if err := store.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Store.Close (seed): %v", err)
	}

	// ---- Phase 2: Full Doll Link interaction path ----
	const mockResponse = "I am Spark. I remember everything that happens."
	const sentText = "Hello Spark, I will remember this."

	log := logger.New(logger.WarnLevel, nil)
	mockProvider := inference.NewMockProvider("test", mockResponse)

	// Open a fresh store (reads the seeded data; MUST not re-read the doll card).
	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}

	svc := interaction.New(store2, mockProvider, log)

	wsSrv := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, svc)

	startErr := make(chan error, 1)
	go func() {
		startErr <- wsSrv.Start(ctx)
	}()

	addr := waitForWSAddr(t, wsSrv)
	select {
	case err := <-startErr:
		t.Fatalf("WS Start returned unexpectedly: %v", err)
	default:
	}

	// Connect over WS and send a message.
	conn := wsConnect(t, addr)
	reqID := uuid.New().String()
	req := events.NewDollMessage(reqID, sparkID, sentText)
	resp := sendAndReceive(t, conn, req, 5*time.Second)
	conn.Close()

	if resp == nil {
		t.Fatal("no response received via WebSocket")
	}

	// ---- Verify response received through Doll Link ----

	// 1. Must be a message type (not system error).
	if resp.Type != events.TypeMessage {
		t.Errorf("resp.Type = %q, want %q", resp.Type, events.TypeMessage)
	}

	// 2. Request/response correlation is correct.
	if resp.CorrelationID != reqID {
		t.Errorf("CorrelationID = %q, want %q", resp.CorrelationID, reqID)
	}

	// 3. Source identifies Core.
	if resp.Source != "core" {
		t.Errorf("Source = %q, want %q", resp.Source, "core")
	}

	// 4. DollID echo.
	if resp.DollID != sparkID {
		t.Errorf("DollID = %q, want %q", resp.DollID, sparkID)
	}

	// 5. Response text is non-empty (WS payload is deserialised to map[string]any).
	payload, ok := resp.Payload.(map[string]any)
	if !ok {
		t.Fatalf("response Payload type = %T, want map[string]any", resp.Payload)
	}
	text, _ := payload["text"].(string)
	if text == "" {
		t.Error("response text is empty")
	}
	if !contains(text, sparkName) {
		t.Errorf("response text = %q, should contain %q", text, sparkName)
	}

	// Shut down the WS server cleanly before closing the store.
	if err := wsSrv.Shutdown(ctx); err != nil {
		t.Fatalf("WS Shutdown: %v", err)
	}

	// ---- Restart boundary: close the store right before Phase 3 ----
	if err := store2.Close(); err != nil {
		t.Fatalf("Store2.Close (restart boundary): %v", err)
	}

	// ---- Phase 3: Fresh store, load Spark, verify same semantic Memory records ----
	store3, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (phase 3): %v", err)
	}
	defer store3.Close()

	reloaded, err := store3.LoadDoll(ctx, sparkID)
	if err != nil {
		t.Fatalf("LoadDoll after reopen: %v", err)
	}

	// Identity must still be intact.
	if reloaded.Identity.DollID != sparkID {
		t.Errorf("reloaded DollID = %q, want %q", reloaded.Identity.DollID, sparkID)
	}

	items := reloaded.Memories.Items

	// 7. After close/reopen, same number of MemoryItems.
	if len(items) != 2 {
		t.Fatalf("after reopen: expected 2 MemoryItems, got %d", len(items))
	}

	human := items[0]
	doll := items[1]

	// Human message: kind, content, sequence, interaction grouping, ID.
	if human.Kind != dollstate.KindHumanMessage {
		t.Errorf("after reopen: items[0].Kind = %q, want %q", human.Kind, dollstate.KindHumanMessage)
	}
	if human.Content != sentText {
		t.Errorf("after reopen: items[0].Content = %q, want %q", human.Content, sentText)
	}
	if human.Sequence != 0 {
		t.Errorf("after reopen: items[0].Sequence = %d, want 0", human.Sequence)
	}
	if human.InteractionID == "" {
		t.Error("after reopen: items[0].InteractionID is empty")
	}
	if human.ID == "" {
		t.Error("after reopen: items[0].ID is empty")
	}

	// Doll response: kind, content, sequence, same interaction, ID.
	if doll.Kind != dollstate.KindDollResponse {
		t.Errorf("after reopen: items[1].Kind = %q, want %q", doll.Kind, dollstate.KindDollResponse)
	}
	if doll.Content != mockResponse {
		t.Errorf("after reopen: items[1].Content = %q, want %q", doll.Content, mockResponse)
	}
	if doll.Sequence != 1 {
		t.Errorf("after reopen: items[1].Sequence = %d, want 1", doll.Sequence)
	}
	if doll.InteractionID != human.InteractionID {
		t.Errorf("after reopen: items[1].InteractionID = %q, want items[0].InteractionID = %q",
			doll.InteractionID, human.InteractionID)
	}
	if doll.ID == "" {
		t.Error("after reopen: items[1].ID is empty")
	}

	// 5. Deterministic ordering: human (Sequence=0) → doll (Sequence=1).
	if human.Sequence >= doll.Sequence {
		t.Errorf("human.Sequence (%d) must be < doll.Sequence (%d)", human.Sequence, doll.Sequence)
	}

	// 8. No Core-local DB row ID required — all identifying fields are
	//    semantic and survive schema-agnostic deserialisation. The test
	//    proves this by reading from a fresh store connection with no
	//    prior in-memory state or cached identifiers.
}