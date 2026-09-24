//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/DollMind"
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

// Phase 4 — Future Agency Continuity Proof
//
// Proves Spark's full cognition pipeline (Enter → L1 Orient → L2 Plan)
// produces a persistent Intention that survives a Store close-and-reopen cycle.
//
// Spark's doll card is enriched with a continuity Drive and an active Goal
// before cognition. A purpose-aware mock provider returns structured JSON:
//
//	Orient: matters=true (the event touches on Spark's continuity Drive)
//	Plan:   request_future_cognition=true (Spark should revisit later)
//
// The mock WakeTime is set far in the future (2035) so validation passes
// regardless of real clock. The intention's Subject and exact WakeTime are
// verified before and after the store restart.
//
// No timer, scheduler job, or goroutine is created — the Intention remains
// a portable semantic record in Doll State at all times.
func TestContinuity_IntentionSurvivesStoreReopen(t *testing.T) {
	ctx := context.Background()

	// ---- Phase 1: Seed the database from Spark's Doll Card ----
	spark := decodeSparkFromCard(t)
	const sparkID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"
	const sentText = "Spark, I want to see if you persist."

	if spark.Identity.DollID != sparkID {
		t.Fatalf("Spark DollID = %q, want %q", spark.Identity.DollID, sparkID)
	}

	// Enrich Spark with a continuity Drive and an active Goal.
	spark.Drives.Items = append(spark.Drives.Items, dollstate.DriveItem{
		ID:          "drive-continuity-001",
		Name:        "Continuity",
		Description: "Spark exists to prove the NeonDoll architecture works correctly through persistence and reload",
	})
	spark.Goals.Items = append(spark.Goals.Items, dollstate.GoalItem{
		ID:          "goal-continuity-001",
		Name:        "Prove self persists across store reopen",
		Description: "Verify that Spark's cognitive state survives a persistence store close-and-reopen cycle",
		State:       dollstate.GoalStateActive,
		DriveID:     "drive-continuity-001",
	})
	if len(spark.Drives.Items) != 1 {
		t.Fatalf("Spark should have 1 Drive, got %d", len(spark.Drives.Items))
	}
	if len(spark.Goals.Items) != 1 {
		t.Fatalf("Spark should have 1 Goal, got %d", len(spark.Goals.Items))
	}

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "neondoll.db")
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

	// ---- Phase 2: Scheduler cognition with purpose-aware mock provider ----
	log := logger.New(logger.WarnLevel, nil)

	// Build deterministic mock responses.
	orientResp, _ := json.Marshal(map[string]any{
		"summary": "User message relates to Spark's continuity drive",
		"matters": true,
		"reason":  "The message touches on proving Spark persists",
	})
	planResp, _ := json.Marshal(map[string]any{
		"summary":                  "Spark should schedule a follow-up reflection",
		"proposed_action":          "Continue continuity verification",
		"observations":             []string{"Continuity drive is active", "Goal to prove persistence is in progress"},
		"should_reorient":          false,
		"request_future_cognition": true,
		"future_subject":           "Verify continuity after store reopen",
		"future_reason":            "Allow time for state persistence before verification",
		"future_wake_time":         "2035-06-15T13:00:00Z",
	})

	purposeProvider := &purposeAwareProvider{
		id: "test",
		responses: map[inference.Purpose]string{
			inference.PurposeOrient: string(orientResp),
			inference.PurposePlan:   string(planResp),
		},
	}

	// Open a fresh store (must not re-read the doll card).
	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}

	// Reload Spark — the enriched state with drive+goal is now in the DB.
	state, err := store2.LoadDoll(ctx, sparkID)
	if err != nil {
		t.Fatalf("LoadDoll after seed: %v", err)
	}

	mindAPI := &storeBackedMindAPI{
		state:    state,
		store:    store2,
		ctx:      ctx,
		provider: purposeProvider,
	}

	sched := dollmind.New(purposeProvider, log, mindAPI)

	// Run the full cognition pipeline: Enter → L1 Orient → L2 Plan
	result, err := sched.Enter(ctx, events.TypeMessage, sentText)
	if err != nil {
		t.Fatalf("Scheduler.Enter: %v", err)
	}

	// Verify cognition reached LevelPlan (both orient matters and plan ran).
	if result.Level != dollmind.LevelPlan {
		t.Fatalf("cognition level = %v, want LevelPlan", result.Level)
	}
	if !result.StateDirty {
		t.Error("StateDirty = false, want true (intention was materialised)")
	}

	// Verify exactly one pending Intention was created.
	intentions := state.Intentions.Items
	if len(intentions) != 1 {
		t.Fatalf("expected 1 pending Intention, got %d", len(intentions))
	}
	intention := intentions[0]
	if intention.ID == "" {
		t.Error("Intention.ID is empty")
	}
	if intention.Subject != "Verify continuity after store reopen" {
		t.Errorf("Intention.Subject = %q, want %q", intention.Subject, "Verify continuity after store reopen")
	}
	if intention.WakeTime != "2035-06-15T13:00:00Z" {
		t.Errorf("Intention.WakeTime = %q, want %q", intention.WakeTime, "2035-06-15T13:00:00Z")
	}
	if intention.State != dollstate.IntentionStatePending {
		t.Errorf("Intention.State = %q, want %q", intention.State, dollstate.IntentionStatePending)
	}

	// ---- Restart boundary: close the store before Phase 3 ----
	if err := store2.Close(); err != nil {
		t.Fatalf("Store2.Close (restart boundary): %v", err)
	}

	// ---- Phase 3: Fresh store, load Spark, verify Intention survived ----
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

	// Drives and Goals must still be intact.
	if len(reloaded.Drives.Items) != 1 {
		t.Errorf("after reopen: expected 1 Drive, got %d", len(reloaded.Drives.Items))
	}
	if len(reloaded.Goals.Items) != 1 {
		t.Errorf("after reopen: expected 1 Goal, got %d", len(reloaded.Goals.Items))
	}

	// The single pending Intention must have survived the close-and-reopen.
	reloadedIntentions := reloaded.Intentions.Items
	if len(reloadedIntentions) != 1 {
		t.Fatalf("after reopen: expected 1 pending Intention, got %d", len(reloadedIntentions))
	}
	ri := reloadedIntentions[0]
	if ri.ID == "" {
		t.Error("after reopen: Intention.ID is empty")
	}
	if ri.Subject != "Verify continuity after store reopen" {
		t.Errorf("after reopen: Intention.Subject = %q, want %q", ri.Subject, "Verify continuity after store reopen")
	}
	if ri.WakeTime != "2035-06-15T13:00:00Z" {
		t.Errorf("after reopen: Intention.WakeTime = %q, want %q", ri.WakeTime, "2035-06-15T13:00:00Z")
	}
	if ri.State != dollstate.IntentionStatePending {
		t.Errorf("after reopen: Intention.State = %q, want %q", ri.State, dollstate.IntentionStatePending)
	}

	// Verify the Intention ID is stable — same one from Phase 2.
	if ri.ID != intention.ID {
		t.Errorf("after reopen: Intention.ID changed: %q → %q", intention.ID, ri.ID)
	}

	// ---- No accidental scheduling ----
	// The Intention is a portable semantic record. No timer, scheduler job,
	// or in-memory handle is created by its mere existence. This test proves
	// that by the Intention surviving a complete Store close+reopen without
	// any Runtime — the intention is just state, not a live object.
}

// purposeAwareProvider returns different JSON responses based on the
// inference Request's Purpose field. Used to drive the cognition pipeline
// deterministically without a live model.
type purposeAwareProvider struct {
	id        inference.ProviderID
	responses map[inference.Purpose]string
}

func (p *purposeAwareProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	resp, ok := p.responses[req.Purpose]
	if !ok {
		return &inference.Response{Content: "", ProviderID: p.id}, nil
	}
	return &inference.Response{
		Content:    resp,
		ProviderID: p.id,
		TokensUsed: len(req.Messages),
	}, nil
}

func (p *purposeAwareProvider) ID() inference.ProviderID { return p.id }

// storeBackedMindAPI implements dollmind.MindAPI backed by a persistence Store.
// Each call to Save() durably persists the held Doll State.
type storeBackedMindAPI struct {
	state    *dollstate.DollState
	store    persistence.Store
	ctx      context.Context
	provider inference.Provider
}

func (m *storeBackedMindAPI) Inference() inference.Provider { return m.provider }
func (m *storeBackedMindAPI) State() *dollstate.DollState   { return m.state }
func (m *storeBackedMindAPI) Save() error {
	return m.store.SaveDoll(m.ctx, m.state)
}
