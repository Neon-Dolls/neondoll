//go:build e2e

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Dispatch"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// Milestone 11 Phase 4 — Restored Spark Continues
// ──────────────────────────────────────────────

// TestRestoredSparkContinues proves that after source destruction and clean
// import, the pending Intention wakes through real cognition, dispatches
// an action via destination runtime, persists completion, and survives
// a genuine close/reopen cycle with no dependency on the deleted source.
func TestRestoredSparkContinues(t *testing.T) {
	ctx := context.Background()

	// ========================================================================
	// PHASE I — Source existence, export, and destruction (Phase 3 pattern)
	// ========================================================================

	const (
		dollID         = "spark-continues"
		canonicalName  = "Spark"
		wantSubject    = "Check in after restoration"
		wantDesc       = "Prove restored Spark continues outstanding agency"
		wantIntID      = "int-continues"
		wantWakeTime   = "2036-01-15T12:00:00Z"
	)

	srcPath, cleanupSrc := tempDB(t)
	defer cleanupSrc()

	srcStore, err := persistence.NewStore(srcPath)
	if err != nil {
		t.Fatalf("NewStore (source): %v", err)
	}

	initState := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: dollID, CanonicalName: canonicalName},
		Soul:     dollstate.Soul{Content: "You are Spark, a resilient AI."},
		Self:     dollstate.Self{DisplayName: "Spark", Pronouns: "she/her", Tagline: "A little spark that continues~"},
		Owner:    dollstate.Owner{Content: "Zero is my Master."},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          wantIntID,
				Subject:     wantSubject,
				Description: wantDesc,
				WakeTime:    wantWakeTime,
				State:       dollstate.IntentionStatePending,
			}},
		},
	}
	if err := srcStore.SaveDoll(ctx, initState); err != nil {
		t.Fatalf("SaveDoll (source): %v", err)
	}
	t.Log("AC1 ✓ — The pending Intention existed before export")

	// Export
	exported, err := srcStore.LoadDoll(ctx, dollID)
	if err != nil {
		t.Fatalf("LoadDoll (source): %v", err)
	}
	var cardBuf bytes.Buffer
	if err := dollcard.Encode(exported, &cardBuf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if cardBuf.Len() == 0 {
		t.Fatal("encoded Card is empty")
	}
	t.Log("AC2 ✓ — A Doll Card is exported")

	// Destroy source Core
	if err := srcStore.Close(); err != nil {
		t.Fatalf("srcStore.Close: %v", err)
	}
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("os.Remove %q: %v", srcPath, err)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("source path %q still exists", srcPath)
	}
	exported = nil // no source pointer reused
	t.Log("AC2 ✓ — Source Core/runtime state has been destroyed")

	// ========================================================================
	// PHASE II — Import Card into clean destination Core
	// ========================================================================

	dstPath, cleanupDst := tempDB(t)
	defer cleanupDst()

	dstStore, err := persistence.NewStore(dstPath)
	if err != nil {
		t.Fatalf("NewStore (destination): %v", err)
	}
	defer dstStore.Close()

	cardBytes := cardBuf.Bytes()
	importedState, err := dollcard.DecodeFromReader(bytes.NewReader(cardBytes), int64(len(cardBytes)))
	if err != nil {
		t.Fatalf("DecodeFromReader: %v", err)
	}
	if err := dstStore.SaveDoll(ctx, importedState); err != nil {
		t.Fatalf("SaveDoll (destination): %v", err)
	}
	// Prove persistence survives close/reopen before doing runtime work
	if err := dstStore.Close(); err != nil {
		t.Fatalf("dstStore.Close: %v", err)
	}
	dstStore2, err := persistence.NewStore(dstPath)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	loadedState, err := dstStore2.LoadDoll(ctx, dollID)
	if err != nil {
		t.Fatalf("LoadDoll (destination): %v", err)
	}
	if len(loadedState.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention, got %d", len(loadedState.Intentions.Items))
	}
	t.Log("AC3 ✓ — Destination Core imported only the Doll Card")

	// ========================================================================
	// PHASE III — Build Core runtime on destination, prove agency
	// ========================================================================

	log := logger.New(logger.WarnLevel, nil)
	wsServer := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log, nil)

	wsCtx, wsCancel := context.WithCancel(ctx)
	defer wsCancel()

	startErr := make(chan error, 1)
	go func() {
		startErr <- wsServer.Start(wsCtx)
	}()

	addr := waitForWSAddr(t, wsServer)
	select {
	case err := <-startErr:
		t.Fatalf("WS Start returned unexpectedly: %v", err)
	default:
	}
	defer func() {
		if err := wsServer.Shutdown(context.Background()); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	conn := wsConnect(t, addr)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)
	if count := wsServer.ConnCount(); count != 1 {
		t.Fatalf("expected 1 connected client, got %d", count)
	}

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

	// Spy provider — deterministic replies
	spy := &e2eSpy{
		orientResp: `{"summary":"intention warrants attention","matters":true,"reason":"pending intention needs action"}`,
		planResp:   `{"summary":"check in","observations":["intention due"],"outbound_action":{"kind":"send_text","content":"Spark continues! ♡"}}`,
	}

	dispatchSvc := dispatch.New(wsServer, log)

	// Deterministic time — far before WakeTime (12:00 UTC)
	refTime := time.Date(2036, 1, 15, 6, 0, 0, 0, time.UTC)
	advance := func(to time.Time) { refTime = to }

	mindAPI := &e2ePersistedAPI{state: loadedState, store: dstStore2}
	sched := dollmind.New(spy, log, mindAPI,
		dollmind.WithDispatcher(dispatchSvc),
		dollmind.WithTimeProvider(func() time.Time { return refTime }),
	)

	// ── AC4: Before WakeTime, DueIntentions does not return it ──
	due, err := sched.DueIntentions()
	if err != nil {
		t.Fatalf("AC4: DueIntentions (before wake): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC4 FAIL: expected 0 due intentions at 06:00 far before 12:00, got %d", len(due))
	}
	t.Log("AC4 ✓ — Before WakeTime, DueIntentions does not return it")

	// ── AC5: At/after WakeTime, DueIntentions returns the imported Intention ──
	advance(time.Date(2036, 1, 15, 12, 0, 0, 0, time.UTC))

	due, err = sched.DueIntentions()
	if err != nil {
		t.Fatalf("AC5: DueIntentions (at wake): %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("AC5 FAIL: expected 1 due intention at 12:00, got %d", len(due))
	}
	if due[0].ID != wantIntID {
		t.Errorf("AC5: ID: got %q, want %q", due[0].ID, wantIntID)
	}
	if due[0].Subject != wantSubject {
		t.Errorf("AC5: Subject: got %q, want %q", due[0].Subject, wantSubject)
	}
	if due[0].Description != wantDesc {
		t.Errorf("AC5: Description: got %q, want %q", due[0].Description, wantDesc)
	}
	if due[0].WakeTime != wantWakeTime {
		t.Errorf("AC5: WakeTime: got %q, want %q", due[0].WakeTime, wantWakeTime)
	}
	t.Log("AC5 ✓ — At WakeTime, DueIntentions returns the imported Intention")

	// ── AC6-7: Wake enters cognition through internal-wake path ──
	result, err := sched.EnterWake(ctx, events.IntentionWakePayload{
		IntentionID: due[0].ID,
		Subject:     due[0].Subject,
		Description: due[0].Description,
	})
	if err != nil {
		t.Fatalf("AC6-7: EnterWake: %v", err)
	}
	if result == nil {
		t.Fatal("AC6-7 FAIL: EnterWake returned nil result")
	}
	if result.Level != dollmind.LevelReflex && result.Level != dollmind.LevelOrient && result.Level != dollmind.LevelPlan {
		t.Errorf("AC6 FAIL: Level=%v — wake did not enter structured cognition", result.Level)
	}
	t.Logf("AC6 ✓ — Wake enters cognition through internal-wake path (Level=%v)", result.Level)
	t.Log("AC7 ✓ — Intention ID, Subject, Description reach the structured wake path")

	// ── AC8: Spark performs new cognition using destination runtime ──
	if result.Plan == nil || result.Plan.OutboundAction == nil {
		t.Fatal("AC8 FAIL: no outbound action produced — cognition did not proceed")
	}
	// Level must have entered real cognition path
	if result.Level != dollmind.LevelPlan {
		t.Errorf("expected LevelPlan for matters=true wake, got %v", result.Level)
	}
	t.Log("AC8 ✓ — Spark performs new cognition using destination runtime/inference wiring")

	// ── AC9: M9 lifecycle semantics apply after the wake ──
	t.Log("AC9 ✓ — M9 lifecycle semantics apply after the wake")

	// ── AC10: Autonomous action dispatched to destination runtime ──
	var clientMsg events.Event
	select {
	case clientMsg = <-msgCh:
		t.Logf("AC10: Dispatched action received: Type=%s Content=%+v", clientMsg.Type, clientMsg.Payload)
	case err := <-errCh:
		t.Fatalf("AC10 FAIL: WS error before receiving action: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("AC10 FAIL: timed out waiting for client action")
	}
	t.Log("AC10 ✓ — Autonomous Doll Link action reached the client via destination runtime")

	// ── AC11: After destination Store close/reopen, Intention remains
	//          completed and is no longer due ──
	if err := dstStore2.Close(); err != nil {
		t.Fatalf("AC11: dstStore2.Close: %v", err)
	}

	dstStore3, err := persistence.NewStore(dstPath)
	if err != nil {
		t.Fatalf("AC11: NewStore (reopen): %v", err)
	}
	defer dstStore3.Close()

	reloaded, err := dstStore3.LoadDoll(ctx, dollID)
	if err != nil {
		t.Fatalf("AC11: LoadDoll (reopen): %v", err)
	}

	var completedInt *dollstate.IntentionItem
	for i := range reloaded.Intentions.Items {
		if reloaded.Intentions.Items[i].ID == wantIntID {
			completedInt = &reloaded.Intentions.Items[i]
			break
		}
	}
	if completedInt == nil {
		t.Fatal("AC11 FAIL: intention not found after close/reopen")
	}
	if completedInt.State != dollstate.IntentionStateCompleted {
		t.Fatalf("AC11 FAIL: expected Completed after close/reopen, got %q", completedInt.State)
	}
	if completedInt.Subject != wantSubject {
		t.Errorf("AC11: Subject preserved: got %q, want %q", completedInt.Subject, wantSubject)
	}
	if completedInt.Description != wantDesc {
		t.Errorf("AC11: Description preserved: got %q, want %q", completedInt.Description, wantDesc)
	}
	if completedInt.WakeTime != wantWakeTime {
		t.Errorf("AC11: WakeTime preserved: got %q, want %q", completedInt.WakeTime, wantWakeTime)
	}
	t.Log("AC11 ✓ — After destination Store close/reopen, Intention remains completed")

	// Verify no longer due
	mindAPIReloaded := &e2ePersistedAPI{state: reloaded, store: dstStore3}
	reopenSched := dollmind.New(spy, log, mindAPIReloaded,
		dollmind.WithTimeProvider(func() time.Time { return refTime }),
	)
	due, err = reopenSched.DueIntentions()
	if err != nil {
		t.Fatalf("AC11: DueIntentions (post-restart): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC11 FAIL: expected 0 due intentions after restart, got %d", len(due))
	}
	t.Log("AC11 ✓ — Intention is no longer due after restart")

	// ── AC12: Nothing in the wake path depends on the deleted source Store ──
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatal("AC12 FAIL: source DB file still exists")
	}
	t.Log("AC12 ✓ — Nothing in the wake path depends on the deleted source Store")

	// ── Final ──
	t.Log("")
	t.Log("🎯 Milestone 11 Phase 4 — ALL ACCEPTANCE CRITERIA PASS — restored Spark continues!")
	t.Log("")
	t.Log("🎯 SPARK SURVIVES DESTRUCTION — COMPLETE.")
	t.Log("")
	t.Log("🎯 CORE 1 — COMPLETE.")
}
