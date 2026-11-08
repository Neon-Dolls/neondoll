package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestReplayKeyLimitPreservesExistingHistory(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider, err := Open(Config{Endpoint: fake.server.URL, DollID: "doll", MaxReplayKeys: 2})
	if err != nil {
		t.Fatal(err)
	}
	revision1 := testRecord("memory", 1, "one")
	revision2 := testRecord("memory", 2, "two")
	revision3 := testRecord("memory", 3, "three")
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: revision1, ReplayKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: revision2, ReplayKey: "request-2"}); err != nil {
		t.Fatal(err)
	}
	rejected, err := provider.Remember(context.Background(), memory.RememberRequest{Record: revision3, ReplayKey: "request-3"})
	if err != nil || rejected.Outcome != memory.RememberRejected || !strings.Contains(rejected.Reason, "replay-key limit") {
		t.Fatalf("limit rejection = %#v, %v", rejected, err)
	}
	current, err := provider.Remember(context.Background(), memory.RememberRequest{Record: revision2, ReplayKey: "request-2"})
	if err != nil || current.Outcome != memory.RememberTransformed {
		t.Fatalf("existing replay = %#v, %v", current, err)
	}
	conflict := revision2
	conflict.Content.Data = json.RawMessage(`"different"`)
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: conflict, ReplayKey: "request-2"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("conflicting existing replay error = %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = Open(Config{Endpoint: fake.server.URL, DollID: "doll", MaxReplayKeys: 2})
	if err != nil {
		t.Fatal(err)
	}
	current, err = provider.Remember(context.Background(), memory.RememberRequest{Record: revision2, ReplayKey: "request-2"})
	if err != nil || current.Outcome != memory.RememberTransformed {
		t.Fatalf("restart replay = %#v, %v", current, err)
	}
	recalled, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "two", Limit: 1})
	if err != nil || len(recalled.Matches) != 1 || recalled.Matches[0].Reference != revision2.Reference() {
		t.Fatalf("retained revision after rejection = %#v, %v", recalled, err)
	}
}

func TestNegativeReplayLimitIsInvalid(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	if _, err := Open(Config{Endpoint: fake.server.URL, DollID: "doll", MaxReplayKeys: -1}); !errors.Is(err, memory.ErrInvalidRequest) {
		t.Fatalf("Open error = %v", err)
	}
}

func TestReplayLimitCountsDistinctKeysForSameRequest(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider, err := Open(Config{Endpoint: fake.server.URL, DollID: "doll", MaxReplayKeys: 2})
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord("memory", 1, "same")
	for _, key := range []string{"request-1", "request-2"} {
		result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: key})
		if err != nil || result.Outcome != memory.RememberTransformed {
			t.Fatalf("key %q = %#v, %v", key, result, err)
		}
	}
	rejected, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: "request-3"})
	if err != nil || rejected.Outcome != memory.RememberRejected {
		t.Fatalf("overflow = %#v, %v", rejected, err)
	}
	for _, key := range []string{"request-1", "request-2"} {
		if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, ReplayKey: key}); err != nil {
			t.Fatalf("existing key %q: %v", key, err)
		}
	}
}
