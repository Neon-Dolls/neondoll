package openviking

import (
	"context"
	"fmt"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestFilteredRecallOverfetchesBoundedCandidates(t *testing.T) {
	f, server := newFake(t)
	p, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: "d", MaxRecallCandidates: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var order []string
	for i := 0; i < 5; i++ {
		r := record(string(rune('a'+i)), 1, "text")
		if i < 4 {
			r.Metadata.Kind = "other"
		}
		if _, err := p.Remember(context.Background(), memory.RememberRequest{Record: r}); err != nil {
			t.Fatal(err)
		}
		order = append(order, p.recordURI(r.ID))
	}
	f.mu.Lock()
	f.searchOrder = append([]string{p.root + "/.relations.json"}, order...)
	f.mu.Unlock()
	got, err := p.Recall(context.Background(), memory.RecallRequest{Query: "text", Limit: 1, Kinds: []string{"observation"}})
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Reference.ID != "e" {
		t.Fatalf("filtered recall: %#v %v", got, err)
	}
	f.mu.Lock()
	limit := f.lastSearchLimit
	f.mu.Unlock()
	if limit != 6 {
		t.Fatalf("candidate bound = %d", limit)
	}
}

func TestRecallCandidateBoundIsExplicit(t *testing.T) {
	f, server := newFake(t)
	p, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: "d", MaxRecallCandidates: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var order []string
	for i := 0; i < 3; i++ {
		r := record(string(rune('a'+i)), 1, "text")
		if i < 2 {
			r.Metadata.Kind = "other"
		}
		_, _ = p.Remember(context.Background(), memory.RememberRequest{Record: r})
		order = append(order, p.recordURI(r.ID))
	}
	f.mu.Lock()
	f.searchOrder = order
	f.mu.Unlock()
	got, err := p.Recall(context.Background(), memory.RecallRequest{Query: "text", Limit: 1, Kinds: []string{"observation"}})
	if err != nil || len(got.Matches) != 0 {
		t.Fatalf("bounded semantic candidate set: %#v %v", got, err)
	}
}

func TestRecallLimitCannotExceedCandidateCeiling(t *testing.T) {
	f, server := newFake(t)
	for _, filtered := range []bool{false, true} {
		p, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: fmt.Sprintf("d-%t", filtered), MaxRecallCandidates: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"one", "two", "three"} {
			if _, err := p.Remember(context.Background(), memory.RememberRequest{Record: record(id, 1, id)}); err != nil {
				t.Fatal(err)
			}
		}
		req := memory.RecallRequest{Query: "text", Limit: 5}
		if filtered {
			req.Kinds = []string{"observation"}
		}
		got, err := p.Recall(context.Background(), req)
		if err != nil || len(got.Matches) != 2 {
			t.Fatalf("filtered=%t: %#v %v", filtered, got, err)
		}
		f.mu.Lock()
		limit := f.lastSearchLimit
		f.mu.Unlock()
		if limit != 2 {
			t.Fatalf("filtered=%t bypassed ceiling: %d", filtered, limit)
		}
		_ = p.Close()
	}
}
