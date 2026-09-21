package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Dispatch"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
)

// fakeSink records every action sent through it.
type fakeSink struct {
	mu      sync.Mutex
	actions []actions.Action
	err     error // if non-nil, SendAction returns this error
}

func (f *fakeSink) SendAction(_ context.Context, action actions.Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.actions = append(f.actions, action)
	return nil
}

func (f *fakeSink) sent() []actions.Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]actions.Action, len(f.actions))
	copy(out, f.actions)
	return out
}

// quietLogger discards log output during tests.
type quietLogger struct{}

func (quietLogger) Info(_ string, _ ...map[string]any) {}
func (quietLogger) Warn(_ string, _ ...map[string]any) {}
func (quietLogger) Error(_ string, _ ...map[string]any) {}

func TestDispatch_SendText_Valid(t *testing.T) {
	sink := &fakeSink{}
	svc := dispatch.New(sink, quietLogger{})
	ctx := context.Background()

	action := dollmind.OutboundAction{
		Kind:    dollmind.ActionKindSendText,
		Content: "Hello, World!",
	}

	if err := svc.Dispatch(ctx, action); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	sent := sink.sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 action sent, got %d", len(sent))
	}
	if sent[0].Type != actions.TypeSendMessage {
		t.Errorf("expected TypeSendMessage, got %q", sent[0].Type)
	}
	payload, ok := sent[0].Payload.(actions.SendMessagePayload)
	if !ok {
		t.Fatalf("expected SendMessagePayload, got %T", sent[0].Payload)
	}
	if payload.Text != "Hello, World!" {
		t.Errorf("expected text %q, got %q", "Hello, World!", payload.Text)
	}
}

func TestDispatch_SendText_EmptyContent(t *testing.T) {
	sink := &fakeSink{}
	svc := dispatch.New(sink, quietLogger{})
	ctx := context.Background()

	action := dollmind.OutboundAction{
		Kind:    dollmind.ActionKindSendText,
		Content: "",
	}

	err := svc.Dispatch(ctx, action)
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if len(sink.sent()) != 0 {
		t.Fatalf("expected zero sink writes on empty content, got %d", len(sink.sent()))
	}
}

func TestDispatch_UnsupportedKind(t *testing.T) {
	sink := &fakeSink{}
	svc := dispatch.New(sink, quietLogger{})
	ctx := context.Background()

	action := dollmind.OutboundAction{
		Kind:    "send_email",
		Content: "test",
	}

	err := svc.Dispatch(ctx, action)
	if err == nil {
		t.Fatal("expected error for unsupported kind, got nil")
	}
	if len(sink.sent()) != 0 {
		t.Fatalf("expected zero sink writes on unsupported kind, got %d", len(sink.sent()))
	}
}

func TestDispatch_SenderError_Surfaced(t *testing.T) {
	sink := &fakeSink{err: errors.New("connection lost")}
	svc := dispatch.New(sink, quietLogger{})
	ctx := context.Background()

	action := dollmind.OutboundAction{
		Kind:    dollmind.ActionKindSendText,
		Content: "Hello",
	}

	err := svc.Dispatch(ctx, action)
	if err == nil {
		t.Fatal("expected error from sender, got nil")
	}
	if !errors.Is(err, errors.New("connection lost")) {
		t.Logf("not a strict Is match, checking contains; error: %v", err)
	}
	if e := err.Error(); e != "dispatch send: connection lost" {
		t.Errorf("expected wrapped error %q, got %q", "dispatch send: connection lost", e)
	}
}

// compile-time check: dispatch.Service implements dollmind.Dispatcher.
var _ dollmind.Dispatcher = (*dispatch.Service)(nil)