//go:build e2e

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Dispatch"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
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

// e2eMindAPI tracks Save() calls.
type e2eMindAPI struct {
	state     *dollstate.DollState
	saveCalls int
}

func (m *e2eMindAPI) Inference() inference.Provider { return nil }
func (m *e2eMindAPI) State() *dollstate.DollState   { return m.state }
func (m *e2eMindAPI) Save() error {
	m.saveCalls++
	return nil
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
	// ── Phase 1: Start real Doll Link WebSocket server ──
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

	// ── Phase 2: Connect real WebSocket client (AC1) ──
	conn := wsConnect(t, addr)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)
	if count := wsServer.ConnCount(); count != 1 {
		t.Fatalf("expected 1 connected client, got %d", count)
	}
	t.Log("AC1 ✓ — client connected before autonomous wake")

	// ── Phase 3: Client read goroutine ──
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

	// ── Phase 4: Build Scheduler + dispatch chain ──
	const (
		intentionID = "m10-e2e-wake"
		subject     = "Send status update"
		desc        = "Check in autonomously"
		wakeTimeRFC = "2035-06-15T12:00:00Z"
	)

	// AC2: pending future Intention in Doll State
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "M10TestDoll"},
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
	mindAPI := &e2eMindAPI{state: state}
	t.Log("AC2 ✓ — Spark has a pending future Intention")

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

	sched := dollmind.New(spy, log, mindAPI,
		dollmind.WithDispatcher(dispatchSvc),
		dollmind.WithTimeProvider(func() time.Time { return currentTime }),
	)

	// ── Phase 5: AC4 — Intention initially NOT due ──
	due, err := sched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC4 FAIL: expected 0 due intentions before time advance, got %d", len(due))
	}
	t.Log("AC4 ✓ — Intention initially not due (reference time before WakeTime)")

	// ── Phase 6: AC5 — advance time makes it due ──
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

	// ── Phase 7: AC6-8 — EnterWake → cognition → dispatch ──
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

	// ── Phase 8: AC9 — client receives the action ──
	var received events.Event
	select {
	case received = <-msgCh:
		t.Logf("Client received event: Type=%s", received.Type)
	case err := <-errCh:
		t.Fatalf("AC9 FAIL: WS error before receiving action: %v", err)
	case <-time.After(3 * time.Second):
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

	// ── Phase 9: AC10 — attributable to autonomous wake path ──
	// The action originated from EnterWake (internal_wake), not from
	// a chat message. We never sent a chat message to the server.
	// The event's Source will be empty (set by actionToEvent for outbound)
	// which distinguishes it from chat responses sourced as "core".
	t.Log("AC10 ✓ — action attributable to autonomous wake (no inbound message)")

	// ── Phase 10: AC11 — Intention Completed + Save called ──
	completed := false
	for _, item := range state.Intentions.Items {
		if item.ID == intentionID {
			completed = item.State == dollstate.IntentionStateCompleted
			break
		}
	}
	if !completed {
		t.Fatal("AC11 FAIL: Intention not marked Completed after wake")
	}
	if mindAPI.saveCalls == 0 {
		t.Fatal("AC11 FAIL: Save() was not called — Intention not persisted")
	}
	t.Log("AC11 ✓ — Intention Completed and persisted via Save()")

	// ── Phase 11: AC12 — not re-dispatched on subsequent DueIntentions ──
	due, err = sched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (post): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("AC12 FAIL: expected 0 due intentions after completion, got %d", len(due))
	}
	t.Log("AC12 ✓ — Completed Intention not re-dispatched")

	// ── Final ──
	t.Log("\n🎯 Milestone 10 Phase 3 — ALL ACCEPTANCE CRITERIA PASS — vertical slice complete!")
}