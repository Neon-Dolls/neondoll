package persistence_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

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
				turnID := session.NewTurnID()
				for item := range batchSize {
					messages[item] = contextbuilder.NewHistoryMessage{
						TurnID:  turnID,
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

func TestAppendSessionMessagesRejectsMissingTurnID(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.New(store.(session.Store))
	created, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.(contextbuilder.HistoryStore).AppendSessionMessages(context.Background(), created.ID, []contextbuilder.NewHistoryMessage{{Role: contextbuilder.RoleUser, Content: "legacy write"}})
	if err != contextbuilder.ErrInvalidRequest {
		t.Fatalf("missing TurnID error = %v", err)
	}
}

func TestSessionMessageTurnIDMigrationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE interaction_sessions (
			session_id TEXT PRIMARY KEY, owner_kind TEXT NOT NULL, owner_instance_id TEXT NOT NULL,
			external_conversation_ref TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE interaction_session_messages (
			session_id TEXT NOT NULL, sequence INTEGER NOT NULL, message_id TEXT NOT NULL UNIQUE,
			role TEXT NOT NULL, content TEXT NOT NULL, created_at TEXT NOT NULL,
			PRIMARY KEY (session_id, sequence)
		);
		INSERT INTO interaction_sessions VALUES ('legacy-session', 'body', 'body-one', '', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO interaction_session_messages VALUES ('legacy-session', 0, 'legacy-user', 'user', 'old question', '2026-01-01T00:00:00Z');
		INSERT INTO interaction_session_messages VALUES ('legacy-session', 1, 'legacy-assistant', 'assistant', 'old answer', '2026-01-01T00:00:01Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	history := store.(contextbuilder.HistoryStore)
	legacy, err := history.ListSessionMessages(context.Background(), session.ID("legacy-session"))
	if err != nil || len(legacy) != 2 || legacy[0].TurnID != "" || legacy[1].TurnID != "" {
		t.Fatalf("migrated legacy history = %+v, %v", legacy, err)
	}
	turnID := session.NewTurnID()
	created, err := history.AppendSessionMessages(context.Background(), session.ID("legacy-session"), []contextbuilder.NewHistoryMessage{
		{TurnID: turnID, Role: contextbuilder.RoleUser, Content: "new question"},
		{TurnID: turnID, Role: contextbuilder.RoleAssistant, Content: "new answer"},
	})
	if err != nil || len(created) != 2 || created[0].TurnID != turnID || created[1].TurnID != turnID {
		t.Fatalf("new turn history = %+v, %v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.(contextbuilder.HistoryStore).ListSessionMessages(context.Background(), session.ID("legacy-session"))
	if err != nil || len(restored) != 4 || restored[2].TurnID != turnID || restored[3].TurnID != turnID {
		t.Fatalf("restored turn history = %+v, %v", restored, err)
	}
	if restored[2].CreatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("invalid restored timestamp: %s", restored[2].CreatedAt)
	}
}
