package openviking

import (
	"context"
	"errors"
	"strings"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestRecordScopedReplaySurvivesUpdatesAndRestart(t *testing.T) {
	_, server := newFake(t)
	p := openTest(t, server.URL, "doll-a")
	ctx := context.Background()
	v1 := record("m1", 1, "one")
	if _, err := p.Remember(ctx, memory.RememberRequest{Record: v1, ReplayKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
	current, err := p.Remember(ctx, memory.RememberRequest{Record: v1, ReplayKey: "request-1"})
	if err != nil || current.Outcome != memory.RememberStored || current.Retained.Revision != 1 {
		t.Fatalf("current replay: %#v %v", current, err)
	}
	v2 := record("m1", 2, "two")
	if _, err := p.Remember(ctx, memory.RememberRequest{Record: v2, ReplayKey: "request-2"}); err != nil {
		t.Fatal(err)
	}
	current, err = p.Remember(ctx, memory.RememberRequest{Record: v2, ReplayKey: "request-2"})
	if err != nil || current.Outcome != memory.RememberStored || current.Retained.Revision != 2 {
		t.Fatalf("latest replay: %#v %v", current, err)
	}
	replayed, err := p.Remember(ctx, memory.RememberRequest{Record: v1, ReplayKey: "request-1"})
	if err != nil || replayed.Outcome != memory.RememberRejected || !strings.Contains(replayed.Reason, "superseded") {
		t.Fatalf("historical replay: %#v %v", replayed, err)
	}
	if _, err = p.Remember(ctx, memory.RememberRequest{Record: record("m1", 1, "changed"), ReplayKey: "request-1"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	_ = p.Close()
	reopened := openTest(t, server.URL, "doll-a")
	replayed, err = reopened.Remember(ctx, memory.RememberRequest{Record: v1, ReplayKey: "request-1"})
	if err != nil || replayed.Outcome != memory.RememberRejected || !strings.Contains(replayed.Reason, "superseded") {
		t.Fatalf("restart replay: %#v %v", replayed, err)
	}
	latest, err := reopened.Recall(ctx, memory.RecallRequest{Query: "two", Limit: 1})
	if err != nil || len(latest.Matches) != 1 || latest.Matches[0].Reference.Revision != 2 {
		t.Fatalf("historical replay changed retained revision: %#v %v", latest, err)
	}
}

func TestReplayScopeAllowsSameKeyAcrossRecordsAndDolls(t *testing.T) {
	_, server := newFake(t)
	a := openTest(t, server.URL, "a")
	b := openTest(t, server.URL, "b")
	for _, tc := range []struct {
		p *Provider
		r memory.Record
	}{{a, record("one", 1, "1")}, {a, record("two", 1, "2")}, {b, record("one", 1, "other doll")}} {
		if _, err := tc.p.Remember(context.Background(), memory.RememberRequest{Record: tc.r, ReplayKey: "same"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTransformationUnsupportedAndReplayBound(t *testing.T) {
	f, server := newFake(t)
	f.mu.Lock()
	f.transform = func(d retainedDocument) retainedDocument { d.Record.Metadata.Kind = "summary"; return d }
	f.mu.Unlock()
	p := openTest(t, server.URL, "a")
	got, err := p.Remember(context.Background(), memory.RememberRequest{Record: record("m", 1, "description")})
	if err != nil || got.Outcome != memory.RememberTransformed || got.Retained.Metadata.Kind != "summary" {
		t.Fatalf("transform: %#v %v", got, err)
	}
	bad := record("bad", 1, "x")
	bad.Content.MediaType = "image/png"
	got, err = p.Remember(context.Background(), memory.RememberRequest{Record: bad})
	if err != nil || got.Outcome != memory.RememberRejected {
		t.Fatalf("reject: %#v %v", got, err)
	}
}
