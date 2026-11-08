package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestRememberRecallTransformationReplayAndRestart(t *testing.T) {
	fake := newFakeHindsight("secret")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll-a")
	record := testRecord("memory-a", 1, "transform: Alice likes tea")
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: "request-a"})
	if err != nil || result.Outcome != memory.RememberTransformed || result.Transformation == nil {
		t.Fatalf("transformed remember = %#v, %v", result, err)
	}
	if result.Retained.Content.MediaType != "application/json" {
		t.Fatalf("retained media type = %q", result.Retained.Content.MediaType)
	}

	replayed, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: "request-a"})
	if err != nil || replayed.Outcome != memory.RememberTransformed {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	changed := record
	changed.Content.Data = json.RawMessage(`"different"`)
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: changed, ReplayKey: "request-a"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}

	revision2 := testRecord("memory-a", 2, "Alice now likes coffee")
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: revision2, ReplayKey: "request-b"}); err != nil {
		t.Fatal(err)
	}
	superseded, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: "request-a"})
	if err != nil || superseded.Outcome != memory.RememberRejected {
		t.Fatalf("superseded replay = %#v, %v", superseded, err)
	}
	_ = provider.Close()
	provider = openTestProvider(t, fake, "doll-a")
	superseded, err = provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: "request-a"})
	if err != nil || superseded.Outcome != memory.RememberRejected {
		t.Fatalf("restart replay = %#v, %v", superseded, err)
	}

	recall, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "coffee", Limit: 5, Kinds: []string{"experience"}})
	if err != nil || len(recall.Matches) != 1 || recall.Matches[0].Record.Source.Type != "interaction" {
		t.Fatalf("recall = %#v, %v", recall, err)
	}
	filtered, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "coffee", Limit: 5, SourceTypes: []string{"interaction"}})
	if err != nil || len(filtered.Matches) != 1 {
		t.Fatalf("source-filtered recall = %#v, %v", filtered, err)
	}
	filtered, err = provider.Recall(context.Background(), memory.RecallRequest{Query: "coffee", Limit: 5, SourceTypes: []string{"sensor"}})
	if err != nil || len(filtered.Matches) != 0 {
		t.Fatalf("excluded recall = %#v, %v", filtered, err)
	}
}

func TestDollIsolationCapabilitiesHealthAndClose(t *testing.T) {
	fake := newFakeHindsight("secret")
	defer fake.close()
	a := openTestProvider(t, fake, "doll-a")
	b := openTestProvider(t, fake, "doll-b")
	if a.bankID == b.bankID {
		t.Fatal("Dolls share a bank")
	}
	if _, err := a.Remember(context.Background(), memory.RememberRequest{Record: testRecord("a", 1, "only A")}); err != nil {
		t.Fatal(err)
	}
	result, err := b.Recall(context.Background(), memory.RecallRequest{Query: "A", Limit: 5})
	if err != nil || len(result.Matches) != 0 {
		t.Fatalf("cross-Doll recall = %#v, %v", result, err)
	}
	capabilities, _ := a.Capabilities(context.Background())
	if capabilities.Consistency.ReadAfterWrite != memory.ReadAfterWriteEventual || capabilities.Consistency.DeterministicOrdering {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if health, err := a.Health(context.Background()); err != nil || health.Status != memory.HealthHealthy {
		t.Fatalf("health = %#v, %v", health, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Health(context.Background()); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("closed health error = %v", err)
	}
}

func TestAuthenticationTimeoutAndUnsupportedContent(t *testing.T) {
	fake := newFakeHindsight("secret")
	defer fake.close()
	bad, err := Open(Config{Endpoint: fake.server.URL, Token: "wrong", DollID: "doll"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Health(context.Background()); err == nil {
		t.Fatal("authentication unexpectedly succeeded")
	}
	provider := openTestProvider(t, fake, "doll")
	record := testRecord("binary", 1, "text")
	record.Content.MediaType = "image/png"
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record})
	if err != nil || result.Outcome != memory.RememberRejected {
		t.Fatalf("unsupported = %#v, %v", result, err)
	}
	fake.delay = 50 * time.Millisecond
	short, _ := Open(Config{Endpoint: fake.server.URL, Token: "secret", DollID: "short", Timeout: time.Millisecond})
	if _, err := short.Health(context.Background()); err == nil {
		t.Fatal("timeout unexpectedly succeeded")
	}
}

func TestMalformedUpstreamResponse(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	fake.malformedRecall = true
	if _, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "x", Limit: 1}); !errors.Is(err, memory.ErrInvalidResult) {
		t.Fatalf("malformed response error = %v", err)
	}
}

func openTestProvider(t *testing.T, fake *fakeHindsight, doll string) *Provider {
	t.Helper()
	provider, err := Open(Config{Endpoint: fake.server.URL, Token: fake.token, DollID: doll, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
func testRecord(id string, revision uint64, text string) memory.Record {
	data, _ := json.Marshal(text)
	return memory.Record{SchemaVersion: 1, ID: id, Revision: revision,
		CreatedAt: time.Unix(100, 0).UTC(), Source: memory.Source{Type: "interaction", Ref: "session"},
		Content:  memory.Content{MediaType: "text/plain", Transport: memory.TransportInline, Data: data},
		Metadata: memory.Metadata{Kind: "experience", Tags: []string{"test"}}}
}
