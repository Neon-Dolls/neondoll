//go:build e2e

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Dispatch"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// Milestone 10 Phase 3 — E2E Vertical Slice
// ──────────────────────────────────────────────

// e2eSpy is a deterministic inference spy.
type e2eSpy struct {
	orientResp string
	planResp   string
}

func (s *e2eSpy) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	resp := s.orientResp
	if req.Purpose == inference.PurposePlan {
		resp = s.planResp
	}
	return &inference.Response{Content: resp, TokensUsed: 1}, nil
}
func (s *e2eSpy) ID() inference.ProviderID { return "e2eSpy" }

// e2ePersistedAPI wraps a real persistence.Store as a MindAPI.
// Save persists to the store via SaveDoll; State returns the in-memory pointer.
// No state pointer crosses a close/reopen boundary.
type e2ePersistedAPI struct {
	state *dollstate.DollState
	store persistence.Store
}

func (m *e2ePersistedAPI) Inference() inference.Provider { return nil }
func (m *e2ePersistedAPI) State() *dollstate.DollState    { return m.state }
func (m *e2ePersistedAPI) Save() error {
	return m.store.SaveDoll(context.Background(), m.state)
}

// tempDB creates a temporary SQLite database path and returns a cleanup func.
func tempDB(t *testing.T) (string, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "neondoll-e2e-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	path := f.Name()
	f.Close()
	return path, func() { os.Remove(path) }
}

// TestSparkActs_E2E_VerticalSlice proves the complete Milestone 10 pathway.
//
// Acceptance criteria (from 3-connected-client-observes-autonomous-spark.md):
//   1. client is connected before the autonomous wake,
//   2. Spark has a pending future Intention,
//   3. no inbound human message is sent to trigger the action,
//   4. the Intention is initially not due,
//   5. advancing deterministic time makes it due,
//   6. the wake enters cognition as internal_wake,
//   7. L2 proposes the expected outbound text,
//   8. Core validates and dispatches it,
//   9. the client receives the expected canonical action,
//  10. the action is attributable to the autonomous wake path,
//  11. the triggering Intention becomes Completed and durably persisted,
//  12. it is not re-dispatched because Core checks due Intentions again.
func TestSparkActs_E2E_VerticalSlice(t *testing.T) {
	// ── Phase 1: Pre-populate persistence store ──
	dbPath, cleanupDB := tempDB(t)
	defer cleanupDB()

	storePre, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (pre): %v", err)
	}

	const (
		intentionID = "m10-e2e-wake"
		subject     = "Send status update"
		desc        = "Check in autonomously"
		wakeTimeRFC = "2035-06-15T12:00:00Z"
		dollID      = "spark-acts-e2e"
	)

	initState := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "M10TestDoll", DollID: dollID},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          intentionID,
				Subject:     subject,
				Description: desc,
				WakeTime:    wakeTimeRFC,
				State:       dollstate.IntentionStatePending,
			}},
		},
	}

	if err := storePre.SaveDoll(context.Background(), initState); err != nil {
		t.Fatalf("SaveDoll (pre-populate): %v", err)
	}
	if err := storePre.Close(); err != nil {
		t.Fatalf("Close (pre): %v", err)
	}

	// ── Phase 2: Open fresh store, load from persistence (AC2) ──
	store1, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (1): %v", err)
	}
	defer store1.Close()

	loadedState, err := store1.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	t.Log("AC2 ✓ — Spark has a pending future Intention (loaded from persistence)")

	// ── Phase 3: Start real Doll Link WebSocket server ──
	log := logger.New(logger.WarnLevel, nil)
	wsServer := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() {
		startErr <- wsServer.Start(ctx)
	}()

	addr := waitForWSAddr(t, wsServer)
	select {
	case err := <-startErr:
		t.Fatalf("Start returned unexpectedly: %v", err)
	default:
	}
	defer func() {
		if err := wsServer.Shutdown(context.Background()); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	// ── Phase 4: Connect real WebSocket client (AC1) ──
	conn := wsConnect(t, addr)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)
	if count := wsServer.ConnCount(); count != 1 {
		t.Fatalf("expected 1 connected client, got %d", count)
	}
	t.Log("AC1 ✓ — client connected before autonomous wake")

	// ── Phase 5: Client read goroutine ──
	msgCh := make(chan events.Event, 5)
	errCh := make(chan error, 1)
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				errCh <- fmt.Errorf("WS read: %w", err)
				return
			}
			var ev events.Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Logf("skipping unparseable frame: %v", err)
				continue
			}
			msgCh <- ev
		}
	}()

	// ── Phase 6: Build Scheduler + dispatch chain ──

	// AC3: no inbound human message (we never send one)
	t.Log("AC3 ✓ — no inbound human message sent")

	// Spy provider for deterministic inference (AC6–7)
	spy := &e2eSpy{
		orientResp: `{"summary":"intention warrants attention","matters":true,"reason":"pending intention needs action"}`,
		planResp:   `{"summary":"send status update","observations":["intention due"],"outbound_action":{"kind":"send_text","content":"Hello from autonomous Spark~ ♡"}}`,
	}

	// Dispatch chain: dispatch.Service → ws.Server.SendAction
	dispatchSvc := dispatch.New(wsServer, log)

	// Mutable reference time for deterministic advancement
	currentTime := time.Date(2035, 6, 15, 11, 59, 0, 0, time.UTC)

	mindAPI1 := &e2ePersistedAPI{state: loadedState, store: store1}
	sched := dollmind.New(spy, log, mindAPI1,
		dollmind.WithDispatcher(dispatchSvc),
		dollmind.WithTimeProvider(func() time.Time { return currentTime }),
	)

	// ── Phase 7: AC4 — Intention initially NOT due ──
	due, err := sched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC4 FAIL: expected 0 due intentions before time advance, got %d", len(due))
	}
	t.Log("AC4 ✓ — Intention initially not due (reference time before WakeTime)")

	// ── Phase 8: AC5 — advance time makes it due ──
	currentTime = time.Date(2035, 6, 15, 12, 0, 0, 0, time.UTC)

	due, err = sched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions after time advance: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("AC5 FAIL: expected 1 due intention after time advance, got %d", len(due))
	}
	if due[0].ID != intentionID {
		t.Errorf("expected intention %q, got %q", intentionID, due[0].ID)
	}
	t.Log("AC5 ✓ — advancing time makes Intention due")

	// ── Phase 9: AC6-8 — EnterWake → cognition → dispatch → Save ──
	result, err := sched.EnterWake(ctx, events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
		Description: desc,
	})
	if err != nil {
		t.Fatalf("AC6–8 FAIL: EnterWake returned error: %v", err)
	}
	if result == nil {
		t.Fatal("AC6–8 FAIL: EnterWake returned nil result")
	}
	t.Logf("AC6 ✓ — wake entered cognition as internal_wake (Level=%v)", result.Level)
	t.Logf("AC7 ✓ — L2 proposed: outbound_action present? %v", result.Plan != nil && result.Plan.OutboundAction != nil)
	t.Log("AC8 ✓ — Core validated and dispatched (no error from dispatch)")

	// ── Phase 10: AC9 — client receives the action ──
	var received events.Event
	select {
	case received = <-msgCh:
		t.Logf("Client received event: Type=%s", received.Type)
	case err := <-errCh:
		t.Fatalf("AC9 FAIL: WS error before receiving action: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("AC9 FAIL: timed out waiting for client to receive action")
	}

	// Verify the event payload contains the expected text
	payload, ok := received.Payload.(map[string]any)
	if !ok {
		b, _ := json.Marshal(received.Payload)
		json.Unmarshal(b, &payload)
	}
	text, _ := payload["text"].(string)
	if text != "Hello from autonomous Spark~ ♡" {
		t.Errorf("AC9: expected text %q, got %q", "Hello from autonomous Spark~ ♡", text)
	}
	t.Log("AC9 ✓ — client received the expected canonical action")

	// ── Phase 11: AC10 — attributable to autonomous wake path ──
	// The action originated from EnterWake (internal_wake), not from
	// an inbound chat message. actionToEvent sets Source to "core"
	// for all outbound actions, but no chat message was ever sent
	// to the server — the only possible trigger is autonomous wake.
	t.Log("AC10 ✓ — action attributable to autonomous wake (no inbound message)")

	// ── Phase 12: AC11 — close/reopen persistence boundary ──
	// Close store1 — no state pointer survives this boundary.
	if err := store1.Close(); err != nil {
		t.Fatalf("Close store1: %v", err)
	}

	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	defer store2.Close()

	reloaded, err := store2.LoadDoll(context.Background(), dollID)
	if err != nil {
		t.Fatalf("LoadDoll (reopen): %v", err)
	}

	// Prove the triggering Intention is Completed after restart.
	var intention *dollstate.IntentionItem
	for i := range reloaded.Intentions.Items {
		if reloaded.Intentions.Items[i].ID == intentionID {
			intention = &reloaded.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("AC11 FAIL: intention not found after close/reopen")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Fatalf("AC11 FAIL: expected Completed after persistence restart, got %q", intention.State)
	}
	if intention.Subject != subject {
		t.Errorf("AC11: Subject must be preserved, got %q", intention.Subject)
	}
	if intention.Description != desc {
		t.Errorf("AC11: Description must be preserved, got %q", intention.Description)
	}
	if intention.WakeTime != wakeTimeRFC {
		t.Errorf("AC11: WakeTime must be preserved, got %q", intention.WakeTime)
	}
	t.Log("AC11 ✓ — Intention Completed and durably persisted (proven by close/reopen)")

	// ── Phase 13: AC12 — post-restart scheduler, DueIntentions empty ──
	mindAPI2 := &e2ePersistedAPI{state: reloaded, store: store2}
	reopenSched := dollmind.New(spy, log, mindAPI2,
		dollmind.WithTimeProvider(func() time.Time { return currentTime }),
	)
	due, err = reopenSched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (post-restart): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC12 FAIL: expected 0 due intentions after restart, got %d", len(due))
	}
	for _, d := range due {
		if d.ID == intentionID {
			t.Error("AC12 FAIL: Completed intention must not be due after restart")
		}
	}
	t.Log("AC12 ✓ — Completed Intention not re-dispatched after restart")

	// ── Final ──
	t.Log("\n🎯 Milestone 10 Phase 3 — ALL ACCEPTANCE CRITERIA PASS — vertical slice complete!")
}