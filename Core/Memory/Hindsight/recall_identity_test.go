package hindsight

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestRecallPreservesPortableIdentityAndGroupsNativeUnits(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	record := testRecord("memory-a", 3, "transform: durable observation")
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record})
	if err != nil || result.Outcome != memory.RememberTransformed {
		t.Fatalf("Remember = %#v, %v", result, err)
	}
	recalled, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "observation", Limit: 10})
	if err != nil || len(recalled.Matches) != 1 {
		t.Fatalf("Recall = %#v, %v", recalled, err)
	}
	got := recalled.Matches[0].Record
	if got.ID != record.ID || got.Revision != record.Revision || got.Source != record.Source ||
		!got.CreatedAt.Equal(record.CreatedAt) || got.Metadata.Kind != record.Metadata.Kind {
		t.Fatalf("portable identity/provenance = %#v, want %#v", got, record)
	}
	if text := string(got.Content.Data); !strings.Contains(text, "extracted one") || !strings.Contains(text, "extracted two") {
		t.Fatalf("native retained view = %s", text)
	}
}

func TestRecallRejectsMissingAndMalformedDocumentIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing", mutate: func(metadata map[string]any) { delete(metadata, stateKey) }},
		{name: "malformed", mutate: func(metadata map[string]any) { metadata[stateKey] = `{"record":` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHindsight("")
			defer fake.close()
			provider := openTestProvider(t, fake, "doll")
			record := testRecord("memory-a", 3, "text")
			if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record}); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			doc := fake.docs[provider.bankID][documentID(record.ID)]
			test.mutate(doc.DocumentMetadata)
			fake.docs[provider.bankID][doc.ID] = doc
			fake.mu.Unlock()
			if _, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "text", Limit: 1}); !errors.Is(err, memory.ErrInvalidResult) {
				t.Fatalf("Recall error = %v", err)
			}
		})
	}
}

func TestRecallCancellationDuringIdentityResolution(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: testRecord("memory-a", 1, "text")}); err != nil {
		t.Fatal(err)
	}
	fake.delayDocument = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := provider.Recall(ctx, memory.RecallRequest{Query: "text", Limit: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recall cancellation error = %v", err)
	}
}
