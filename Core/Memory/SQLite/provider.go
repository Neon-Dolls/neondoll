// Package sqlite provides the local SQLite implementation of the Core-owned
// Memory Provider contract. Its schema is private adapter state, not a
// portable memory representation.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"

	_ "modernc.org/sqlite"
)

const (
	DefaultDatabasePath          = "neondoll-memory.sqlite"
	DefaultMaxInlineBytes uint64 = 4 << 20
)

type Config struct {
	Path           string
	DollID         string
	MaxInlineBytes uint64
}

type Provider struct {
	db             *sql.DB
	dollID         string
	maxInlineBytes uint64
	closeOnce      sync.Once
	closeErr       error
}

var _ memory.Provider = (*Provider)(nil)

func Open(config Config) (*Provider, error) {
	config.Path = strings.TrimSpace(config.Path)
	config.DollID = strings.TrimSpace(config.DollID)
	if config.Path == "" {
		config.Path = DefaultDatabasePath
	}
	if config.DollID == "" {
		return nil, fmt.Errorf("%w: doll ID is required", memory.ErrInvalidRequest)
	}
	if config.MaxInlineBytes == 0 {
		config.MaxInlineBytes = DefaultMaxInlineBytes
	}
	if config.Path != ":memory:" {
		parent := filepath.Dir(config.Path)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, unavailable("create database directory", err)
		}
	}
	db, err := sql.Open("sqlite", config.Path)
	if err != nil {
		return nil, unavailable("open database", err)
	}
	db.SetMaxOpenConns(1)
	provider := &Provider{db: db, dollID: config.DollID, maxInlineBytes: config.MaxInlineBytes}
	if err := provider.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if config.Path != ":memory:" {
		if err := os.Chmod(config.Path, 0o600); err != nil {
			_ = db.Close()
			return nil, unavailable("protect database", err)
		}
	}
	return provider, nil
}

func (p *Provider) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS memory_provider_records (
			doll_id TEXT NOT NULL,
			memory_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			envelope BLOB NOT NULL,
			search_text TEXT NOT NULL,
			metadata_kind TEXT NOT NULL,
			source_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			experienced_at TEXT,
			PRIMARY KEY (doll_id, memory_id)
		)`,
		`CREATE INDEX IF NOT EXISTS memory_provider_recall
			ON memory_provider_records (doll_id, experienced_at DESC, created_at DESC, memory_id)`,
		`CREATE TABLE IF NOT EXISTS memory_provider_replays (
			doll_id TEXT NOT NULL,
			replay_key TEXT NOT NULL,
			memory_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			envelope_sha256 TEXT NOT NULL,
			PRIMARY KEY (doll_id, replay_key)
		)`,
	} {
		if _, err := p.db.ExecContext(ctx, statement); err != nil {
			return unavailable("initialize database", err)
		}
	}
	return nil
}

func (p *Provider) Close() error {
	p.closeOnce.Do(func() { p.closeErr = p.db.Close() })
	return p.closeErr
}

func (p *Provider) Remember(ctx context.Context, request memory.RememberRequest) (memory.RememberResult, error) {
	if err := ctx.Err(); err != nil {
		return memory.RememberResult{}, err
	}
	if err := request.Record.Validate(); err != nil || (request.Record.Content.Transport == memory.TransportStream) != (request.Payload != nil) {
		if err != nil {
			return memory.RememberResult{}, err
		}
		return memory.RememberResult{}, memory.ErrInvalidRequest
	}
	if reason := p.unsupportedReason(request.Record); reason != "" {
		return memory.RememberResult{Outcome: memory.RememberRejected, Reason: reason}, nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.RememberResult{}, operationError(ctx, "begin remember", err)
	}
	result, err := p.rememberTx(ctx, tx, request.Record, request.ReplayKey)
	if err != nil {
		_ = tx.Rollback()
		return memory.RememberResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return memory.RememberResult{}, operationError(ctx, "commit remember", err)
	}
	return result, nil
}

func (p *Provider) rememberTx(ctx context.Context, tx *sql.Tx, record memory.Record, replayKey string) (memory.RememberResult, error) {
	envelope, err := memory.EncodeRecord(record)
	if err != nil {
		return memory.RememberResult{}, err
	}
	digest := sha256.Sum256(envelope)
	digestText := hex.EncodeToString(digest[:])
	if replayKey != "" {
		if strings.TrimSpace(replayKey) == "" {
			return memory.RememberResult{}, memory.ErrInvalidRequest
		}
		var id, hash string
		var revision uint64
		err := tx.QueryRowContext(ctx, `SELECT memory_id, revision, envelope_sha256 FROM memory_provider_replays WHERE doll_id=? AND replay_key=?`, p.dollID, replayKey).Scan(&id, &revision, &hash)
		if err == nil {
			if id != record.ID || revision != record.Revision || hash != digestText {
				return memory.RememberResult{}, fmt.Errorf("%w: replay key identifies different content", memory.ErrConflict)
			}
			retained, err := p.loadTx(ctx, tx, id)
			if err != nil {
				return memory.RememberResult{}, err
			}
			return stored(retained), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return memory.RememberResult{}, unavailable("read replay", err)
		}
	}

	existing, err := p.loadTx(ctx, tx, record.ID)
	switch {
	case err == nil:
		disposition, compareErr := memory.CompareImportRevision(existing, record)
		if compareErr != nil {
			return memory.RememberResult{Outcome: memory.RememberRejected, Reason: compareErr.Error()}, nil
		}
		if disposition == memory.ImportIdempotent {
			if err := p.insertReplay(ctx, tx, replayKey, record, digestText); err != nil {
				return memory.RememberResult{}, err
			}
			return stored(existing), nil
		}
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return memory.RememberResult{}, err
	}

	searchText, err := searchableText(record)
	if err != nil {
		return memory.RememberResult{}, err
	}
	var experienced any
	if record.ExperiencedAt != nil {
		experienced = record.ExperiencedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_provider_records
		(doll_id,memory_id,revision,envelope,search_text,metadata_kind,source_type,created_at,experienced_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(doll_id,memory_id) DO UPDATE SET
			revision=excluded.revision,envelope=excluded.envelope,search_text=excluded.search_text,
			metadata_kind=excluded.metadata_kind,source_type=excluded.source_type,
			created_at=excluded.created_at,experienced_at=excluded.experienced_at`,
		p.dollID, record.ID, record.Revision, envelope, searchText, record.Metadata.Kind, record.Source.Type,
		record.CreatedAt.UTC().Format(time.RFC3339Nano), experienced)
	if err != nil {
		return memory.RememberResult{}, unavailable("store memory", err)
	}
	if replayKey != "" {
		if err := p.insertReplay(ctx, tx, replayKey, record, digestText); err != nil {
			return memory.RememberResult{}, err
		}
	}
	return stored(record), nil
}

func (p *Provider) insertReplay(ctx context.Context, tx *sql.Tx, replayKey string, record memory.Record, digest string) error {
	if replayKey == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_provider_replays (doll_id,replay_key,memory_id,revision,envelope_sha256) VALUES(?,?,?,?,?)`, p.dollID, replayKey, record.ID, record.Revision, digest); err != nil {
		return operationError(ctx, "store replay", err)
	}
	return nil
}

func stored(record memory.Record) memory.RememberResult {
	copy := record
	return memory.RememberResult{Outcome: memory.RememberStored, Retained: &copy}
}

func (p *Provider) loadTx(ctx context.Context, tx *sql.Tx, id string) (memory.Record, error) {
	var envelope []byte
	err := tx.QueryRowContext(ctx, `SELECT envelope FROM memory_provider_records WHERE doll_id=? AND memory_id=?`, p.dollID, id).Scan(&envelope)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.Record{}, sql.ErrNoRows
		}
		return memory.Record{}, unavailable("load memory", err)
	}
	record, err := memory.DecodeRecord(envelope)
	if err != nil {
		return memory.Record{}, unavailable("decode retained memory", err)
	}
	return record, nil
}

func (p *Provider) Recall(ctx context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
	if err := request.Validate(); err != nil {
		return memory.RecallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return memory.RecallResult{}, err
	}
	query := `SELECT envelope FROM memory_provider_records WHERE doll_id=?`
	args := []any{p.dollID}
	if request.Query != "" {
		query += ` AND instr(lower(search_text), lower(?)) > 0`
		args = append(args, request.Query)
	}
	query, args = addStringFilter(query, args, "metadata_kind", request.Kinds)
	query, args = addStringFilter(query, args, "source_type", request.SourceTypes)
	query += ` ORDER BY COALESCE(experienced_at,created_at) DESC, created_at DESC, memory_id ASC LIMIT ?`
	args = append(args, request.Limit)
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return memory.RecallResult{}, operationError(ctx, "recall", err)
	}
	defer rows.Close()
	result := memory.RecallResult{Matches: make([]memory.RecallMatch, 0)}
	for rows.Next() {
		var envelope []byte
		if err := rows.Scan(&envelope); err != nil {
			return memory.RecallResult{}, unavailable("scan recall", err)
		}
		record, err := memory.DecodeRecord(envelope)
		if err != nil {
			return memory.RecallResult{}, unavailable("decode recall", err)
		}
		copy := record
		result.Matches = append(result.Matches, memory.RecallMatch{Reference: record.Reference(), Record: &copy})
	}
	if err := rows.Err(); err != nil {
		if ctx.Err() != nil {
			return memory.RecallResult{}, ctx.Err()
		}
		return memory.RecallResult{}, unavailable("iterate recall", err)
	}
	return result, nil
}

func addStringFilter(query string, args []any, column string, values []string) (string, []any) {
	if len(values) == 0 {
		return query, args
	}
	query += " AND " + column + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + ")"
	for _, value := range values {
		args = append(args, value)
	}
	return query, args
}

func (p *Provider) Export(ctx context.Context, request memory.ExportRequest, sink memory.ExportSink) (memory.ExportReport, error) {
	bounded, err := memory.NewBoundedExportSink(request, sink)
	if err != nil {
		return memory.ExportReport{}, err
	}
	rows, err := p.db.QueryContext(ctx, `SELECT envelope FROM memory_provider_records WHERE doll_id=? ORDER BY memory_id`, p.dollID)
	if err != nil {
		return memory.ExportReport{}, operationError(ctx, "export", err)
	}
	defer rows.Close()
	report := memory.ExportReport{}
	records := make([]memory.Record, 0)
	for rows.Next() {
		var envelope []byte
		if err := rows.Scan(&envelope); err != nil {
			return exportUsage(report, bounded), unavailable("scan export", err)
		}
		record, err := memory.DecodeRecord(envelope)
		if err != nil {
			return exportUsage(report, bounded), unavailable("decode export", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		if ctx.Err() != nil {
			return exportUsage(report, bounded), ctx.Err()
		}
		return exportUsage(report, bounded), unavailable("iterate export", err)
	}
	if err := rows.Close(); err != nil {
		return exportUsage(report, bounded), unavailable("close export query", err)
	}
	for _, record := range records {
		if err := bounded.Put(ctx, memory.Transfer{Record: record}); err != nil {
			return exportUsage(report, bounded), err
		}
	}
	report = exportUsage(report, bounded)
	report.Completed = true
	return report, nil
}

func exportUsage(report memory.ExportReport, bounded *memory.BoundedExportSink) memory.ExportReport {
	report.Records, report.Bytes = bounded.Usage()
	return report
}

func (p *Provider) Import(ctx context.Context, request memory.ImportRequest, source memory.ImportSource) (memory.ImportReport, error) {
	bounded, err := memory.NewBoundedImportSource(request, source)
	if err != nil {
		return memory.ImportReport{}, err
	}
	report := memory.ImportReport{}
	transfers := make([]memory.Transfer, 0)
	for {
		transfer, err := bounded.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.Records, report.Bytes = bounded.Usage()
			return report, err
		}
		if transfer.Payload != nil {
			if _, err := memory.CopyBounded(ctx, memory.Writer(io.Discard), transfer.Payload, request.MaxBytes); err != nil {
				report.Records, report.Bytes = bounded.Usage()
				return report, err
			}
		}
		transfers = append(transfers, transfer)
	}
	report.Records, report.Bytes = bounded.Usage()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return report, operationError(ctx, "begin import", err)
	}
	rollback := func(cause error) (memory.ImportReport, error) {
		_ = tx.Rollback()
		retained := report.Outcomes[:0]
		for _, outcome := range report.Outcomes {
			if outcome.Disposition == memory.ImportStored {
				report.Failures = append(report.Failures, memory.TransferFailure{Reference: outcome.Source, Reason: "import transaction rolled back"})
				continue
			}
			retained = append(retained, outcome)
		}
		report.Outcomes = retained
		return report, cause
	}
	for _, transfer := range transfers {
		record := transfer.Record
		if reason := p.unsupportedReason(record); reason != "" {
			report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Disposition: memory.ImportRejected, Reason: reason})
			continue
		}
		existing, loadErr := p.loadTx(ctx, tx, record.ID)
		if loadErr == nil {
			disposition, compareErr := memory.CompareImportRevision(existing, record)
			if compareErr != nil {
				report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Disposition: memory.ImportRejected, Reason: compareErr.Error()})
				continue
			}
			if disposition == memory.ImportIdempotent {
				ref := existing.Reference()
				report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Retained: &ref, Disposition: memory.ImportIdempotent})
				continue
			}
		} else if !errors.Is(loadErr, sql.ErrNoRows) {
			return rollback(loadErr)
		}
		result, rememberErr := p.rememberTx(ctx, tx, record, "")
		if rememberErr != nil {
			return rollback(rememberErr)
		}
		if result.Outcome == memory.RememberRejected {
			report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Disposition: memory.ImportRejected, Reason: result.Reason})
			continue
		}
		ref := result.Retained.Reference()
		report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Retained: &ref, Disposition: memory.ImportStored})
	}
	if err := tx.Commit(); err != nil {
		return report, operationError(ctx, "commit import", err)
	}
	report.Completed = true
	return report, nil
}

func (p *Provider) Capabilities(ctx context.Context) (memory.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return memory.Capabilities{}, err
	}
	return memory.Capabilities{
		ContractVersion: memory.ContractVersion,
		ContentTypes:    []string{"application/json", "text/plain"},
		MaxInlineBytes:  p.maxInlineBytes,
		SupportsUpdates: true,
	}, nil
}

func (p *Provider) Health(ctx context.Context) (memory.Health, error) {
	if err := ctx.Err(); err != nil {
		return memory.Health{}, err
	}
	if err := p.db.PingContext(ctx); err != nil {
		return memory.Health{Status: memory.HealthUnavailable, Detail: "SQLite storage is unavailable"}, nil
	}
	return memory.Health{Status: memory.HealthHealthy, Detail: "SQLite storage is available"}, nil
}

func (p *Provider) unsupportedReason(record memory.Record) string {
	mediaType, _, err := mime.ParseMediaType(record.Content.MediaType)
	if err != nil {
		return "invalid content media type"
	}
	if mediaType != "text/plain" && mediaType != "application/json" {
		return "unsupported content type: " + mediaType
	}
	if record.Content.Transport != memory.TransportInline {
		return "SQLite provider currently requires inline text or JSON content"
	}
	if uint64(len(record.Content.Data)) > p.maxInlineBytes {
		return "inline content exceeds provider limit"
	}
	return ""
}

func searchableText(record memory.Record) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(record.Content.MediaType)
	if mediaType == "text/plain" {
		var text string
		if err := json.Unmarshal(record.Content.Data, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	return string(record.Content.Data), nil
}

func unavailable(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", memory.ErrUnavailable, operation, err)
}

func operationError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return unavailable(operation, err)
}
