package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

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
	if err != nil || capabilities.ContractVersion != memory.ContractVersion || capabilities.SupportsUpdates != true || capabilities.MaxStreamBytes != 0 {
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
	report, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 1, MaxBytes: 1 << 20}, &transferSink{})
	if !errors.Is(err, memory.ErrTransferLimit) || report.Completed || report.Records != 1 {
		t.Fatalf("bounded export=%+v err=%v", report, err)
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
