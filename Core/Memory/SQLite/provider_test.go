package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func createLegacyReplayDatabase(t *testing.T, path string, records map[string]memory.Record) map[string][]byte {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE memory_provider_records (
			doll_id TEXT NOT NULL,memory_id TEXT NOT NULL,revision INTEGER NOT NULL,envelope BLOB NOT NULL,
			search_text TEXT NOT NULL,metadata_kind TEXT NOT NULL,source_type TEXT NOT NULL,
			created_at TEXT NOT NULL,experienced_at TEXT,PRIMARY KEY (doll_id,memory_id))`,
		`CREATE INDEX memory_provider_recall ON memory_provider_records (doll_id, experienced_at DESC, created_at DESC, memory_id)`,
		`CREATE TABLE memory_provider_replays (
			doll_id TEXT NOT NULL,replay_key TEXT NOT NULL,memory_id TEXT NOT NULL,revision INTEGER NOT NULL,
			envelope_sha256 TEXT NOT NULL,PRIMARY KEY (doll_id,replay_key))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	envelopes := make(map[string][]byte, len(records))
	for dollID, record := range records {
		envelope, err := memory.EncodeRecord(record)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(envelope)
		if _, err := db.Exec(`INSERT INTO memory_provider_records
			(doll_id,memory_id,revision,envelope,search_text,metadata_kind,source_type,created_at,experienced_at)
			VALUES(?,?,?,?,?,?,?,?,NULL)`, dollID, record.ID, record.Revision, envelope, "legacy text", record.Metadata.Kind, record.Source.Type, record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO memory_provider_replays
			(doll_id,replay_key,memory_id,revision,envelope_sha256) VALUES(?,?,?,?,?)`, dollID, "legacy-replay", record.ID, record.Revision, hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
		envelopes[dollID] = envelope
	}
	return envelopes
}

func databaseColumns(t *testing.T, path, table string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func testRecord(id string, revision uint64, content string, created time.Time) memory.Record {
	return memory.Record{
		SchemaVersion: memory.SchemaVersion,
		ID:            id,
		Revision:      revision,
		CreatedAt:     created.UTC(),
		Source:        memory.Source{Type: "interaction", Ref: "turn-" + id},
		Content: memory.Content{
			MediaType: "text/plain; charset=utf-8",
			Transport: memory.TransportInline,
			Data:      json.RawMessage(strconvQuote(content)),
		},
		Metadata: memory.Metadata{Kind: "conversation", Subject: "subject-" + id},
	}
}

func streamRecord(id string, revision uint64, mediaType string, payload []byte, created time.Time) memory.Record {
	size := int64(len(payload))
	return memory.Record{
		SchemaVersion: memory.SchemaVersion,
		ID:            id,
		Revision:      revision,
		CreatedAt:     created.UTC(),
		Source:        memory.Source{Type: "interaction", Ref: "turn-" + id},
		Content:       memory.Content{MediaType: mediaType, Transport: memory.TransportStream, Size: &size},
		Metadata:      memory.Metadata{Kind: "conversation", Subject: "subject-" + id},
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func openTestProvider(t *testing.T, path, dollID string) *Provider {
	t.Helper()
	provider, err := Open(Config{Path: path, DollID: dollID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func remember(t *testing.T, provider *Provider, record memory.Record) memory.RememberResult {
	t.Helper()
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func recall(t *testing.T, provider *Provider, query string, limit uint32) memory.RecallResult {
	t.Helper()
	result, err := provider.Recall(context.Background(), memory.RecallRequest{Query: query, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRememberRecallTextAndJSON(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	text := testRecord("text", 1, "the neon fox", base)
	jsonRecord := testRecord("json", 1, "unused", base.Add(time.Minute))
	jsonRecord.Content.MediaType = "application/json"
	jsonRecord.Content.Data = json.RawMessage(`{"topic":"constellation","count":2}`)

	for _, record := range []memory.Record{text, jsonRecord} {
		result := remember(t, provider, record)
		if result.Outcome != memory.RememberStored || result.Retained == nil || result.Retained.Reference() != record.Reference() {
			t.Fatalf("remember=%+v", result)
		}
	}
	if result := recall(t, provider, "neon", 10); len(result.Matches) != 1 || result.Matches[0].Reference != text.Reference() {
		t.Fatalf("text recall=%+v", result)
	}
	if result := recall(t, provider, "constellation", 10); len(result.Matches) != 1 || result.Matches[0].Record.Content.MediaType != "application/json" {
		t.Fatalf("JSON recall=%+v", result)
	}
	all := recall(t, provider, "", 10)
	if len(all.Matches) != 2 || all.Matches[0].Reference.ID != "json" || all.Matches[1].Reference.ID != "text" {
		t.Fatalf("deterministic ordering=%+v", all)
	}
}

func TestPersistenceAcrossCloseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	record := testRecord("persistent", 3, "survives restart", time.Now().UTC())
	first, err := Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	remember(t, first, record)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTestProvider(t, path, "doll-a")
	result := recall(t, second, "restart", 1)
	if len(result.Matches) != 1 || result.Matches[0].Record.Source != record.Source || result.Matches[0].Record.Revision != 3 {
		t.Fatalf("reopened=%+v", result)
	}
}

func TestLegacyReplaySchemaMigrationPreservesRecordsAndDollIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	created := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	recordA := testRecord("shared-memory", 1, "legacy alpha", created)
	recordB := testRecord("shared-memory", 1, "legacy beta", created.Add(time.Second))
	envelopes := createLegacyReplayDatabase(t, path, map[string]memory.Record{"doll-a": recordA, "doll-b": recordB})

	providerA, err := Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	requestA := memory.RememberRequest{Record: recordA, ReplayKey: "legacy-replay"}
	if result, err := providerA.Remember(context.Background(), requestA); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("migrated replay=%+v err=%v", result, err)
	}
	different := recordA
	different.Content.Data = json.RawMessage(`"different"`)
	if _, err := providerA.Remember(context.Background(), memory.RememberRequest{Record: different, ReplayKey: "legacy-replay"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("legacy replay accepted different content: %v", err)
	}
	if got := recall(t, providerA, "", 10); len(got.Matches) != 1 || string(got.Matches[0].Record.Content.Data) != `"legacy alpha"` {
		t.Fatalf("Doll A memory changed: %+v", got)
	}
	if err := providerA.Close(); err != nil {
		t.Fatal(err)
	}

	providerB, err := Open(Config{Path: path, DollID: "doll-b"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := providerB.Remember(context.Background(), memory.RememberRequest{Record: recordB, ReplayKey: "legacy-replay"}); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("Doll B migrated replay=%+v err=%v", result, err)
	}
	if got := recall(t, providerB, "", 10); len(got.Matches) != 1 || string(got.Matches[0].Record.Content.Data) != `"legacy beta"` {
		t.Fatalf("Doll B memory changed: %+v", got)
	}
	if err := providerB.Close(); err != nil {
		t.Fatal(err)
	}

	columns := databaseColumns(t, path, "memory_provider_replays")
	if !columns["digest_scheme"] || !columns["digest_sha256"] || columns["envelope_sha256"] || columns["retained_sha256"] {
		t.Fatalf("replay columns=%v", columns)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for dollID, envelope := range envelopes {
		var retainedEnvelope []byte
		var digest string
		var scheme string
		if err := db.QueryRow(`SELECT m.envelope,r.digest_scheme,r.digest_sha256 FROM memory_provider_records m
			JOIN memory_provider_replays r ON r.doll_id=m.doll_id AND r.memory_id=m.memory_id
			WHERE m.doll_id=? AND r.replay_key='legacy-replay'`, dollID).Scan(&retainedEnvelope, &scheme, &digest); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(retainedEnvelope, envelope) || scheme != replayDigestLegacyEnvelope || digest != legacyEnvelopeDigest(envelope) {
			t.Fatalf("Doll %s migration changed record or digest", dollID)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	providerA, err = Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if result, err := providerA.Remember(context.Background(), requestA); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("reopened replay=%+v err=%v", result, err)
	}
	if err := providerA.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMigrationPreservesSupersededReplayIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-history.db")
	created := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	revision1 := map[string]memory.Record{
		"doll-a": testRecord("memory-a", 1, "alpha revision one", created),
		"doll-b": testRecord("memory-a", 1, "beta revision one", created),
	}
	createLegacyReplayDatabase(t, path, revision1)
	revision2 := map[string]memory.Record{
		"doll-a": testRecord("memory-a", 2, "alpha revision two", created),
		"doll-b": testRecord("memory-a", 2, "beta revision two", created),
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for dollID, record := range revision2 {
		envelope, err := memory.EncodeRecord(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE memory_provider_replays SET replay_key='request-1' WHERE doll_id=? AND replay_key='legacy-replay'`, dollID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE memory_provider_records SET revision=?,envelope=?,search_text=? WHERE doll_id=? AND memory_id=?`, record.Revision, envelope, "revision two", dollID, record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO memory_provider_replays
			(doll_id,replay_key,memory_id,revision,envelope_sha256) VALUES(?,?,?,?,?)`,
			dollID, "request-2", record.ID, record.Revision, legacyEnvelopeDigest(envelope)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	verify := func(t *testing.T, dollID string) {
		t.Helper()
		provider, err := Open(Config{Path: path, DollID: dollID})
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		old := revision1[dollID]
		oldReplay, err := provider.Remember(context.Background(), memory.RememberRequest{Record: old, ReplayKey: "request-1"})
		if err != nil || oldReplay.Outcome != memory.RememberRejected || oldReplay.Retained != nil || !strings.Contains(oldReplay.Reason, "superseded") {
			t.Fatalf("historical replay=%+v err=%v", oldReplay, err)
		}
		current := revision2[dollID]
		currentReplay, err := provider.Remember(context.Background(), memory.RememberRequest{Record: current, ReplayKey: "request-2"})
		if err != nil || currentReplay.Outcome != memory.RememberStored || currentReplay.Retained == nil || currentReplay.Retained.Revision != 2 {
			t.Fatalf("current replay=%+v err=%v", currentReplay, err)
		}
		changedOld := old
		changedOld.Content.Data = json.RawMessage(`"different old content"`)
		if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: changedOld, ReplayKey: "request-1"}); !errors.Is(err, memory.ErrConflict) {
			t.Fatalf("request-1 accepted different content: %v", err)
		}
		changedCurrent := current
		changedCurrent.Content.Data = json.RawMessage(`"different current content"`)
		if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: changedCurrent, ReplayKey: "request-2"}); !errors.Is(err, memory.ErrConflict) {
			t.Fatalf("request-2 accepted different content: %v", err)
		}
		withoutReplay, err := provider.Remember(context.Background(), memory.RememberRequest{Record: old})
		if err != nil || withoutReplay.Outcome != memory.RememberRejected || !strings.Contains(withoutReplay.Reason, "stale revision") {
			t.Fatalf("old revision overwrite=%+v err=%v", withoutReplay, err)
		}
		retained := recall(t, provider, "", 10)
		if len(retained.Matches) != 1 || retained.Matches[0].Record.Revision != 2 || !bytes.Equal(retained.Matches[0].Record.Content.Data, current.Content.Data) {
			t.Fatalf("retained memory=%+v", retained)
		}
	}

	t.Run("doll-a first open", func(t *testing.T) { verify(t, "doll-a") })
	t.Run("doll-b isolated", func(t *testing.T) { verify(t, "doll-b") })
	t.Run("doll-a reopen", func(t *testing.T) { verify(t, "doll-a") })
}

func TestLegacyReplayMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-failure.db")
	record := testRecord("legacy", 1, "must remain", time.Now().UTC())
	createLegacyReplayDatabase(t, path, map[string]memory.Record{"doll-a": record})
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memory_provider_replays_new (blocker TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{Path: path, DollID: "doll-a"}); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("migration unexpectedly succeeded: %v", err)
	}
	if columns := databaseColumns(t, path, "memory_provider_records"); columns["payload"] {
		t.Fatalf("payload migration was not rolled back: %v", columns)
	}
	if columns := databaseColumns(t, path, "memory_provider_replays"); !columns["envelope_sha256"] || columns["digest_sha256"] || columns["digest_scheme"] {
		t.Fatalf("legacy replay table was changed: %v", columns)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var replayCount int
	if err := db.QueryRow(`SELECT count(*) FROM memory_provider_replays WHERE doll_id='doll-a' AND replay_key='legacy-replay'`).Scan(&replayCount); err != nil || replayCount != 1 {
		t.Fatalf("legacy replay lost: count=%d err=%v", replayCount, err)
	}
	if _, err := db.Exec(`DROP TABLE memory_provider_replays_new`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatalf("migration after removing injected failure: %v", err)
	}
	_ = provider.Close()
}

func TestNewReplaySchemaIsCurrentAndReopenDoesNotMutateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	provider, err := Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	columns := databaseColumns(t, path, "memory_provider_replays")
	if !columns["digest_scheme"] || !columns["digest_sha256"] || columns["envelope_sha256"] || columns["retained_sha256"] {
		t.Fatalf("new replay columns=%v", columns)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	provider, err = Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var after, temporaryTables int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='memory_provider_replays_new'`).Scan(&temporaryTables); err != nil {
		t.Fatal(err)
	}
	if before != after || temporaryTables != 0 {
		t.Fatalf("reopen mutated schema: before=%d after=%d temporary=%d", before, after, temporaryTables)
	}
}

func TestRecallBoundsFiltersAndCancellation(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		record := testRecord(string(rune('a'+i)), 1, "shared search", base.Add(time.Duration(i)*time.Second))
		if i == 2 {
			record.Metadata.Kind = "observation"
			record.Source.Type = "sensor"
		}
		remember(t, provider, record)
	}
	result, err := provider.Recall(context.Background(), memory.RecallRequest{Query: "shared", Limit: 1})
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("bounded recall=%+v err=%v", result, err)
	}
	result, err = provider.Recall(context.Background(), memory.RecallRequest{Limit: 10, Kinds: []string{"observation"}, SourceTypes: []string{"sensor"}})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Reference.ID != "c" {
		t.Fatalf("filtered recall=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Recall(ctx, memory.RecallRequest{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

type transferSink struct{ transfers []memory.Transfer }

func (s *transferSink) Put(_ context.Context, transfer memory.Transfer) error {
	s.transfers = append(s.transfers, transfer)
	return nil
}

type errorSink struct{ err error }

func (s errorSink) Put(context.Context, memory.Transfer) error { return s.err }

type cancellingPayloadSink struct{ cancel context.CancelFunc }

func (s cancellingPayloadSink) Put(ctx context.Context, transfer memory.Transfer) error {
	buffer := make([]byte, 1)
	if _, err := transfer.Payload.ReadContext(ctx, buffer); err != nil {
		return err
	}
	s.cancel()
	_, err := memory.CopyBounded(ctx, memory.Writer(io.Discard), transfer.Payload, 1<<20)
	return err
}

type payloadSink struct {
	records  []memory.Record
	payloads [][]byte
}

func (s *payloadSink) Put(ctx context.Context, transfer memory.Transfer) error {
	s.records = append(s.records, transfer.Record)
	if transfer.Payload == nil {
		s.payloads = append(s.payloads, nil)
		return nil
	}
	var payload bytes.Buffer
	if _, err := memory.CopyBounded(ctx, memory.Writer(&payload), transfer.Payload, 64<<20); err != nil {
		return err
	}
	s.payloads = append(s.payloads, append([]byte(nil), payload.Bytes()...))
	return nil
}

type transferSource struct {
	transfers []memory.Transfer
	index     int
}

func (s *transferSource) Next(context.Context) (memory.Transfer, error) {
	if s.index == len(s.transfers) {
		return memory.Transfer{}, io.EOF
	}
	transfer := s.transfers[s.index]
	s.index++
	return transfer, nil
}

func TestExportImportPortableRecords(t *testing.T) {
	base := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	source := openTestProvider(t, filepath.Join(t.TempDir(), "source.db"), "doll-a")
	records := []memory.Record{testRecord("one", 1, "first", base), testRecord("two", 2, "second", base.Add(time.Second))}
	for _, record := range records {
		remember(t, source, record)
	}
	sink := &transferSink{}
	exportReport, err := source.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink)
	if err != nil || !exportReport.Completed || len(sink.transfers) != 2 {
		t.Fatalf("export=%+v transfers=%d err=%v", exportReport, len(sink.transfers), err)
	}
	for _, transfer := range sink.transfers {
		encoded, err := memory.EncodeRecord(transfer.Record)
		if err != nil || strings.Contains(string(encoded), "memory_provider_records") || strings.Contains(string(encoded), "rowid") {
			t.Fatalf("provider detail leaked: %s err=%v", encoded, err)
		}
	}
	target := openTestProvider(t, filepath.Join(t.TempDir(), "target.db"), "doll-a")
	importReport, err := target.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &transferSource{transfers: sink.transfers})
	if err != nil || !importReport.Completed || len(importReport.Outcomes) != 2 {
		t.Fatalf("import=%+v err=%v", importReport, err)
	}
	got := recall(t, target, "", 10)
	if len(got.Matches) != 2 || got.Matches[1].Record.Source != records[0].Source || got.Matches[0].Record.Revision != 2 {
		t.Fatalf("imported=%+v", got)
	}
}

func TestStreamedTextRememberRecallExportImport(t *testing.T) {
	payload := []byte("streamed neon memory\nwith another line")
	record := streamRecord("stream-text", 1, "text/plain; charset=utf-8", payload, time.Now().UTC())
	source := openTestProvider(t, filepath.Join(t.TempDir(), "source.db"), "doll-a")
	result, err := source.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(payload))})
	if err != nil || result.Outcome != memory.RememberStored || result.Retained == nil || result.Retained.Reference() != record.Reference() {
		t.Fatalf("remember=%+v err=%v", result, err)
	}
	recalled := recall(t, source, "another line", 10)
	if len(recalled.Matches) != 1 || recalled.Matches[0].Record.Content.Transport != memory.TransportStream || recalled.Matches[0].Record.Source != record.Source {
		t.Fatalf("recall=%+v", recalled)
	}
	exported := &payloadSink{}
	report, err := source.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, exported)
	if err != nil || !report.Completed || len(exported.records) != 1 || !bytes.Equal(exported.payloads[0], payload) {
		t.Fatalf("export=%+v payload=%q err=%v", report, exported.payloads, err)
	}
	target := openTestProvider(t, filepath.Join(t.TempDir(), "target.db"), "doll-a")
	transfer := memory.Transfer{Record: exported.records[0], Payload: memory.Reader(bytes.NewReader(exported.payloads[0]))}
	imported, err := target.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &transferSource{transfers: []memory.Transfer{transfer}})
	if err != nil || !imported.Completed || len(imported.Outcomes) != 1 || imported.Outcomes[0].Disposition != memory.ImportStored {
		t.Fatalf("import=%+v err=%v", imported, err)
	}
	secondExport := &payloadSink{}
	if _, err := target.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, secondExport); err != nil || !bytes.Equal(secondExport.payloads[0], payload) {
		t.Fatalf("imported payload=%q err=%v", secondExport.payloads, err)
	}
}

func TestStreamedJSONPreservesExactBytes(t *testing.T) {
	payload := []byte("{\n  \"nested\": [1, 2],\n  \"enabled\": true\n}\n")
	record := streamRecord("stream-json", 1, "application/json", payload, time.Now().UTC())
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(payload))})
	if err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("remember=%+v err=%v", result, err)
	}
	sink := &payloadSink{}
	if _, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.payloads) != 1 || !bytes.Equal(sink.payloads[0], payload) {
		t.Fatalf("JSON bytes changed: %q", sink.payloads)
	}
}

func TestStreamValidationAndSizeLimits(t *testing.T) {
	provider, err := Open(Config{Path: filepath.Join(t.TempDir(), "memory.db"), DollID: "doll-a", MaxStreamBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	oversizedPayload := []byte("12345")
	oversized := streamRecord("large", 1, "text/plain", oversizedPayload, time.Now().UTC())
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: oversized, Payload: memory.Reader(bytes.NewReader(oversizedPayload))})
	if err != nil || result.Outcome != memory.RememberRejected || !strings.Contains(result.Reason, "exceeds") {
		t.Fatalf("oversized=%+v err=%v", result, err)
	}
	invalidJSON := []byte(`[`)
	invalid := streamRecord("invalid-json", 1, "application/json", invalidJSON, time.Now().UTC())
	result, err = provider.Remember(context.Background(), memory.RememberRequest{Record: invalid, Payload: memory.Reader(bytes.NewReader(invalidJSON))})
	if err != nil || result.Outcome != memory.RememberRejected || !strings.Contains(result.Reason, "JSON") {
		t.Fatalf("invalid JSON=%+v err=%v", result, err)
	}
	wrongSizePayload := []byte("four")
	wrongSize := streamRecord("wrong-size", 1, "text/plain", wrongSizePayload, time.Now().UTC())
	declared := int64(3)
	wrongSize.Content.Size = &declared
	result, err = provider.Remember(context.Background(), memory.RememberRequest{Record: wrongSize, Payload: memory.Reader(bytes.NewReader(wrongSizePayload))})
	if err != nil || result.Outcome != memory.RememberRejected || !strings.Contains(result.Reason, "size") {
		t.Fatalf("wrong size=%+v err=%v", result, err)
	}
}

func TestStreamCancellation(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	payload := []byte("blocked")
	record := streamRecord("cancelled", 1, "text/plain", payload, time.Now().UTC())
	started := make(chan struct{})
	reader := memory.StreamReaderFunc(func(ctx context.Context, _ []byte) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := provider.Remember(ctx, memory.RememberRequest{Record: record, Payload: reader})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream read did not unblock on cancellation")
	}
}

func TestExportCancellationDuringStreamDelivery(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	payload := []byte("cancel while exporting")
	record := streamRecord("export-cancel", 1, "text/plain", payload, time.Now().UTC())
	if result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(payload))}); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("remember=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	report, err := provider.Export(ctx, memory.ExportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, cancellingPayloadSink{cancel: cancel})
	if !errors.Is(err, context.Canceled) || report.Completed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestStreamRevisionAndReplayKeyIncludePayload(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	firstPayload := []byte("first payload")
	record := streamRecord("stream-replay", 1, "text/plain", firstPayload, time.Now().UTC())
	request := memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(firstPayload)), ReplayKey: "stream-request"}
	if result, err := provider.Remember(context.Background(), request); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("first=%+v err=%v", result, err)
	}
	envelope, err := memory.EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	var scheme, digest string
	if err := provider.db.QueryRow(`SELECT digest_scheme,digest_sha256 FROM memory_provider_replays WHERE doll_id='doll-a' AND replay_key='stream-request'`).Scan(&scheme, &digest); err != nil {
		t.Fatal(err)
	}
	if scheme != replayDigestRetained || digest != retainedDigest(envelope, firstPayload) {
		t.Fatalf("stream replay identity: scheme=%q digest=%q", scheme, digest)
	}
	request.Payload = memory.Reader(bytes.NewReader(firstPayload))
	if result, err := provider.Remember(context.Background(), request); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	different := []byte("other payload")
	request.Payload = memory.Reader(bytes.NewReader(different))
	if _, err := provider.Remember(context.Background(), request); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("replay accepted different payload: %v", err)
	}
	if result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(different))}); err != nil || result.Outcome != memory.RememberRejected {
		t.Fatalf("same revision accepted different payload: %+v err=%v", result, err)
	}
	newerPayload := []byte("newer payload")
	newer := streamRecord("stream-replay", 2, "text/plain", newerPayload, record.CreatedAt)
	if result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: newer, Payload: memory.Reader(bytes.NewReader(newerPayload))}); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("newer=%+v err=%v", result, err)
	}
}

func TestRevisionConflictsCannotOverwriteNewerRecord(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	created := time.Now().UTC()
	newer := testRecord("same", 2, "newer", created)
	older := testRecord("same", 1, "older", created)
	remember(t, provider, newer)
	result := remember(t, provider, older)
	if result.Outcome != memory.RememberRejected || !strings.Contains(result.Reason, "stale revision") {
		t.Fatalf("stale result=%+v", result)
	}
	got := recall(t, provider, "", 1)
	if got.Matches[0].Record.Revision != 2 || string(got.Matches[0].Record.Content.Data) != `"newer"` {
		t.Fatalf("newer overwritten: %+v", got)
	}

	source := &transferSource{transfers: []memory.Transfer{{Record: older}}}
	report, err := provider.Import(context.Background(), memory.ImportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, source)
	if err != nil || len(report.Outcomes) != 1 || report.Outcomes[0].Disposition != memory.ImportRejected {
		t.Fatalf("stale import=%+v err=%v", report, err)
	}
}

func TestRememberReplayKeyIsDurableAndBoundToContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	record := testRecord("replay", 1, "stable replay", time.Now().UTC())
	provider, err := Open(Config{Path: path, DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	request := memory.RememberRequest{Record: record, ReplayKey: "request-1"}
	if result, err := provider.Remember(context.Background(), request); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("first replay=%+v err=%v", result, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider = openTestProvider(t, path, "doll-a")
	if result, err := provider.Remember(context.Background(), request); err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("durable replay=%+v err=%v", result, err)
	}
	changed := record
	changed.Revision = 2
	changed.Content.Data = json.RawMessage(`"different"`)
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: changed, ReplayKey: "request-1"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("replay key accepted different content: %v", err)
	}
}

func TestUnsupportedAndMalformedRecordsAreRejected(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	unsupported := testRecord("image", 1, "not an image", time.Now().UTC())
	unsupported.Content = memory.Content{MediaType: "image/png", Transport: memory.TransportStream}
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: unsupported, Payload: memory.Reader(strings.NewReader("png"))})
	if err != nil || result.Outcome != memory.RememberRejected || result.Reason == "" {
		t.Fatalf("unsupported=%+v err=%v", result, err)
	}
	malformed := testRecord("bad", 1, "bad", time.Now().UTC())
	malformed.Revision = 0
	if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: malformed}); !errors.Is(err, memory.ErrInvalidRecord) {
		t.Fatalf("malformed=%v", err)
	}
}

func TestImportRollsBackOnSourceFailure(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	valid := testRecord("valid", 1, "must roll back", time.Now().UTC())
	failing := testRecord("fail", 1, "trigger storage failure", time.Now().UTC())
	if _, err := provider.db.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON memory_provider_records WHEN NEW.memory_id='fail' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	report, err := provider.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &transferSource{transfers: []memory.Transfer{{Record: valid}, {Record: failing}}})
	if !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("import error=%v", err)
	}
	if len(report.Outcomes) != 0 || len(report.Failures) != 1 || report.Failures[0].Reference != valid.Reference() {
		t.Fatalf("rollback report=%+v", report)
	}
	if got := recall(t, provider, "", 10); len(got.Matches) != 0 {
		t.Fatalf("partial import committed: %+v", got)
	}
}

func TestDollStorageIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	a := openTestProvider(t, path, "doll-a")
	b := openTestProvider(t, path, "doll-b")
	created := time.Now().UTC()
	remember(t, a, testRecord("same-id", 1, "alpha", created))
	remember(t, b, testRecord("same-id", 1, "beta", created))
	if got := recall(t, a, "", 10); len(got.Matches) != 1 || string(got.Matches[0].Record.Content.Data) != `"alpha"` {
		t.Fatalf("doll a=%+v", got)
	}
	if got := recall(t, b, "", 10); len(got.Matches) != 1 || string(got.Matches[0].Record.Content.Data) != `"beta"` {
		t.Fatalf("doll b=%+v", got)
	}
}

func TestHealthCapabilitiesAndUnavailableDatabase(t *testing.T) {
	provider, err := Open(Config{Path: filepath.Join(t.TempDir(), "memory.db"), DollID: "doll-a"})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil || capabilities.ContractVersion != memory.ContractVersion || capabilities.SupportsUpdates != true || capabilities.MaxStreamBytes != DefaultMaxStreamBytes {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
	health, err := provider.Health(context.Background())
	if err != nil || health.Status != memory.HealthHealthy {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	health, err = provider.Health(context.Background())
	if err != nil || health.Status != memory.HealthUnavailable {
		t.Fatalf("closed health=%+v err=%v", health, err)
	}
	if _, err := provider.Recall(context.Background(), memory.RecallRequest{Limit: 1}); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("closed recall=%v", err)
	}
}

func TestExportBoundsAndCancellation(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	created := time.Now().UTC()
	remember(t, provider, testRecord("one", 1, "one", created))
	remember(t, provider, testRecord("two", 1, "two", created.Add(time.Second)))
	// Corrupt a later row. A genuinely incremental bounded export must stop at
	// the record limit without loading or decoding that row.
	if _, err := provider.db.Exec(`UPDATE memory_provider_records SET envelope=x'00' WHERE doll_id='doll-a' AND memory_id='two'`); err != nil {
		t.Fatal(err)
	}
	sink := &transferSink{}
	report, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 1, MaxBytes: 1 << 20}, sink)
	if !errors.Is(err, memory.ErrTransferLimit) || report.Completed || report.Records != 1 {
		t.Fatalf("bounded export=%+v err=%v", report, err)
	}
	if len(sink.transfers) != 1 || sink.transfers[0].Record.ID != "one" {
		t.Fatalf("incremental sink=%+v", sink.transfers)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Export(ctx, memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &transferSink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled export=%v", err)
	}
	if _, err := provider.Import(ctx, memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &transferSource{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import=%v", err)
	}
}

func TestExportPropagatesSinkBackpressureWithIncompleteReport(t *testing.T) {
	provider := openTestProvider(t, filepath.Join(t.TempDir(), "memory.db"), "doll-a")
	remember(t, provider, testRecord("one", 1, "one", time.Now().UTC()))
	backpressure := errors.New("sink unavailable")
	report, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, errorSink{err: backpressure})
	if !errors.Is(err, backpressure) || report.Completed || report.Records != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
