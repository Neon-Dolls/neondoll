package persistence_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

func TestAppendSessionMessagesConcurrentBatches(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	history := store.(contextbuilder.HistoryStore)
	sessions := session.New(store.(session.Store))
	ctx := context.Background()
	first, err := sessions.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sessions.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-two"}})
	if err != nil {
		t.Fatal(err)
	}

	const (
		batches   = 24
		batchSize = 3
	)
	type appendResult struct {
		sessionID session.ID
		batch     int
		messages  []contextbuilder.HistoryMessage
		err       error
	}
	results := make(chan appendResult, batches*2)
	var wg sync.WaitGroup
	for _, sessionID := range []session.ID{first.ID, second.ID} {
		for batch := range batches {
			wg.Add(1)
			go func(sessionID session.ID, batch int) {
				defer wg.Done()
				messages := make([]contextbuilder.NewHistoryMessage, batchSize)
				for item := range batchSize {
					messages[item] = contextbuilder.NewHistoryMessage{
						Role:    contextbuilder.RoleUser,
						Content: fmt.Sprintf("%s/batch-%02d/item-%d", sessionID, batch, item),
					}
				}
				created, err := history.AppendSessionMessages(ctx, sessionID, messages)
				results <- appendResult{sessionID: sessionID, batch: batch, messages: created, err: err}
			}(sessionID, batch)
		}
	}
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Errorf("append session %s batch %d: %v", result.sessionID, result.batch, result.err)
			continue
		}
		if len(result.messages) != batchSize {
			t.Errorf("append session %s batch %d returned %d messages", result.sessionID, result.batch, len(result.messages))
			continue
		}
		for item, message := range result.messages {
			if message.SessionID != result.sessionID {
				t.Errorf("batch %d message %d session = %s, want %s", result.batch, item, message.SessionID, result.sessionID)
			}
			if item > 0 && message.Sequence != result.messages[item-1].Sequence+1 {
				t.Errorf("batch %d sequences are not contiguous: %d then %d", result.batch, result.messages[item-1].Sequence, message.Sequence)
			}
			wantSuffix := fmt.Sprintf("/batch-%02d/item-%d", result.batch, item)
			if !strings.HasSuffix(message.Content, wantSuffix) {
				t.Errorf("batch %d message %d content = %q, want suffix %q", result.batch, item, message.Content, wantSuffix)
			}
		}
	}

	for _, current := range []*session.InteractionSession{first, second} {
		messages, err := history.ListSessionMessages(ctx, current.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != batches*batchSize {
			t.Fatalf("session %s has %d messages, want %d", current.ID, len(messages), batches*batchSize)
		}
		for sequence, message := range messages {
			if message.Sequence != sequence {
				t.Fatalf("session %s message %d sequence = %d", current.ID, sequence, message.Sequence)
			}
			if message.SessionID != current.ID || !strings.HasPrefix(message.Content, string(current.ID)+"/") {
				t.Fatalf("session %s contains foreign message %+v", current.ID, message)
			}
		}
	}
}
