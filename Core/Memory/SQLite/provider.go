// Package sqlite provides the local SQLite implementation of the Core-owned
// Memory Provider contract. Its schema is private adapter state, not a
// portable memory representation.
package sqlite

import (
	"bytes"
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
	"unicode/utf8"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"

	_ "modernc.org/sqlite"
)

const (
	DefaultDatabasePath               = "neondoll-memory.sqlite"
	DefaultMaxInlineBytes      uint64 = 4 << 20
	DefaultMaxStreamBytes      uint64 = 16 << 20
	replayDigestLegacyEnvelope        = "legacy-envelope-v1"
	replayDigestRetained              = "retained-v1"
)

type Config struct {
	Path           string
	DollID         string
	MaxInlineBytes uint64
	MaxStreamBytes uint64
}

type Provider struct {
	db             *sql.DB
	dollID         string
	maxInlineBytes uint64
	maxStreamBytes uint64
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
	if config.MaxStreamBytes == 0 {
		config.MaxStreamBytes = DefaultMaxStreamBytes
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
	provider := &Provider{db: db, dollID: config.DollID, maxInlineBytes: config.MaxInlineBytes, maxStreamBytes: config.MaxStreamBytes}
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
			payload BLOB,
			PRIMARY KEY (doll_id, memory_id)
		)`,
		`CREATE INDEX IF NOT EXISTS memory_provider_recall
			ON memory_provider_records (doll_id, experienced_at DESC, created_at DESC, memory_id)`,
		`CREATE TABLE IF NOT EXISTS memory_provider_replays (
			doll_id TEXT NOT NULL,
			replay_key TEXT NOT NULL,
			memory_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			digest_scheme TEXT NOT NULL,
			digest_sha256 TEXT NOT NULL,
			PRIMARY KEY (doll_id, replay_key)
		)`,
	} {
		if _, err := p.db.ExecContext(ctx, statement); err != nil {
			return unavailable("initialize database", err)
		}
	}
	return p.migrateSchema(ctx)
}

func (p *Provider) migrateSchema(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return operationError(ctx, "begin schema migration", err)
	}
	rollback := func(operation string, cause error) error {
		_ = tx.Rollback()
		return operationError(ctx, operation, cause)
	}
	recordColumns, err := tableColumns(ctx, tx, "memory_provider_records")
	if err != nil {
		return rollback("inspect memory schema", err)
	}
	if !recordColumns["payload"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE memory_provider_records ADD COLUMN payload BLOB`); err != nil {
			return rollback("migrate stream payload schema", err)
		}
	}
	replayColumns, err := tableColumns(ctx, tx, "memory_provider_replays")
	if err != nil {
		return rollback("inspect replay schema", err)
	}
	switch {
	case replayColumns["digest_scheme"] && replayColumns["digest_sha256"]:
		// Current schema. Reopening is intentionally a no-op.
	case replayColumns["envelope_sha256"]:
		if err := p.migrateReplayDigests(ctx, tx, "envelope_sha256", replayDigestLegacyEnvelope); err != nil {
			return rollback("migrate replay schema", err)
		}
	case replayColumns["retained_sha256"]:
		// Compatibility with databases created by the reviewed, unmerged
		// payload-aware schema. These values already use retainedDigest.
		if err := p.migrateReplayDigests(ctx, tx, "retained_sha256", replayDigestRetained); err != nil {
			return rollback("migrate replay schema", err)
		}
	default:
		return rollback("inspect replay schema", fmt.Errorf("missing replay digest column"))
	}
	if err := tx.Commit(); err != nil {
		return operationError(ctx, "commit schema migration", err)
	}
	return nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", table)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (p *Provider) migrateReplayDigests(ctx context.Context, tx *sql.Tx, sourceColumn, scheme string) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE memory_provider_replays_new (
		doll_id TEXT NOT NULL,
		replay_key TEXT NOT NULL,
		memory_id TEXT NOT NULL,
		revision INTEGER NOT NULL,
		digest_scheme TEXT NOT NULL,
		digest_sha256 TEXT NOT NULL,
		PRIMARY KEY (doll_id, replay_key)
	)`); err != nil {
		return err
	}
	lastDollID, lastReplayKey := "", ""
	for {
		var dollID, replayKey, memoryID string
		var revision uint64
		var digest string
		query := fmt.Sprintf(`SELECT doll_id,replay_key,memory_id,revision,%s
			FROM memory_provider_replays
			WHERE doll_id>? OR (doll_id=? AND replay_key>?)
			ORDER BY doll_id,replay_key LIMIT 1`, sourceColumn)
		// Column names are selected only from the two constants above.
		err := tx.QueryRowContext(ctx, query, lastDollID, lastDollID, lastReplayKey).
			Scan(&dollID, &replayKey, &memoryID, &revision, &digest)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		if len(digest) != sha256.Size*2 {
			return fmt.Errorf("replay %q for Doll %q has invalid digest", replayKey, dollID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_provider_replays_new
			(doll_id,replay_key,memory_id,revision,digest_scheme,digest_sha256) VALUES(?,?,?,?,?,?)`,
			dollID, replayKey, memoryID, revision, scheme, digest); err != nil {
			return err
		}
		lastDollID, lastReplayKey = dollID, replayKey
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE memory_provider_replays`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE memory_provider_replays_new RENAME TO memory_provider_replays`); err != nil {
		return err
	}
	return nil
}

type retainedMemory struct {
	record  memory.Record
	payload []byte
}

type importCandidate struct {
	retained  retainedMemory
	rejection string
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
	payload, reason, err := p.prepareContent(ctx, request.Record, request.Payload)
	if err != nil {
		return memory.RememberResult{}, err
	}
	if reason != "" {
		return memory.RememberResult{Outcome: memory.RememberRejected, Reason: reason}, nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.RememberResult{}, operationError(ctx, "begin remember", err)
	}
	result, err := p.rememberTx(ctx, tx, retainedMemory{record: request.Record, payload: payload}, request.ReplayKey)
	if err != nil {
		_ = tx.Rollback()
		return memory.RememberResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return memory.RememberResult{}, operationError(ctx, "commit remember", err)
	}
	return result, nil
}

func (p *Provider) rememberTx(ctx context.Context, tx *sql.Tx, retained retainedMemory, replayKey string) (memory.RememberResult, error) {
	record := retained.record
	envelope, err := memory.EncodeRecord(record)
	if err != nil {
		return memory.RememberResult{}, err
	}
	digestText := retainedDigest(envelope, retained.payload)
	if replayKey != "" {
		if strings.TrimSpace(replayKey) == "" {
			return memory.RememberResult{}, memory.ErrInvalidRequest
		}
		var id, scheme, hash string
		var revision uint64
		err := tx.QueryRowContext(ctx, `SELECT memory_id,revision,digest_scheme,digest_sha256 FROM memory_provider_replays WHERE doll_id=? AND replay_key=?`, p.dollID, replayKey).Scan(&id, &revision, &scheme, &hash)
		if err == nil {
			expectedDigest := digestText
			if scheme == replayDigestLegacyEnvelope {
				expectedDigest = legacyEnvelopeDigest(envelope)
			} else if scheme != replayDigestRetained {
				return memory.RememberResult{}, unavailable("read replay", fmt.Errorf("unknown digest scheme %q", scheme))
			}
			if id != record.ID || revision != record.Revision || hash != expectedDigest {
				return memory.RememberResult{}, fmt.Errorf("%w: replay key identifies different content", memory.ErrConflict)
			}
			existing, err := p.loadTx(ctx, tx, id)
			if err != nil {
				return memory.RememberResult{}, err
			}
			if existing.record.Revision > record.Revision {
				return memory.RememberResult{Outcome: memory.RememberRejected, Reason: fmt.Sprintf("replay recognized; revision %d is superseded by retained revision %d", record.Revision, existing.record.Revision)}, nil
			}
			if existing.record.Revision != record.Revision {
				return memory.RememberResult{}, fmt.Errorf("%w: replay revision is not retained", memory.ErrConflict)
			}
			return stored(existing.record), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return memory.RememberResult{}, unavailable("read replay", err)
		}
	}

	existing, err := p.loadTx(ctx, tx, record.ID)
	switch {
	case err == nil:
		disposition, compareErr := compareRetained(existing, retained)
		if compareErr != nil {
			return memory.RememberResult{Outcome: memory.RememberRejected, Reason: compareErr.Error()}, nil
		}
		if disposition == memory.ImportIdempotent {
			if err := p.insertReplay(ctx, tx, replayKey, record, digestText); err != nil {
				return memory.RememberResult{}, err
			}
			return stored(existing.record), nil
		}
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return memory.RememberResult{}, err
	}

	searchText, err := searchableText(record, retained.payload)
	if err != nil {
		return memory.RememberResult{}, err
	}
	var experienced any
	if record.ExperiencedAt != nil {
		experienced = record.ExperiencedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_provider_records
		(doll_id,memory_id,revision,envelope,search_text,metadata_kind,source_type,created_at,experienced_at,payload)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(doll_id,memory_id) DO UPDATE SET
			revision=excluded.revision,envelope=excluded.envelope,search_text=excluded.search_text,
			metadata_kind=excluded.metadata_kind,source_type=excluded.source_type,
			created_at=excluded.created_at,experienced_at=excluded.experienced_at,payload=excluded.payload`,
		p.dollID, record.ID, record.Revision, envelope, searchText, record.Metadata.Kind, record.Source.Type,
		record.CreatedAt.UTC().Format(time.RFC3339Nano), experienced, retained.payload)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_provider_replays (doll_id,replay_key,memory_id,revision,digest_scheme,digest_sha256) VALUES(?,?,?,?,?,?)`, p.dollID, replayKey, record.ID, record.Revision, replayDigestRetained, digest); err != nil {
		return operationError(ctx, "store replay", err)
	}
	return nil
}

func stored(record memory.Record) memory.RememberResult {
	copy := record
	return memory.RememberResult{Outcome: memory.RememberStored, Retained: &copy}
}

func (p *Provider) loadTx(ctx context.Context, tx *sql.Tx, id string) (retainedMemory, error) {
	var envelope, payload []byte
	err := tx.QueryRowContext(ctx, `SELECT envelope, payload FROM memory_provider_records WHERE doll_id=? AND memory_id=?`, p.dollID, id).Scan(&envelope, &payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return retainedMemory{}, sql.ErrNoRows
		}
		return retainedMemory{}, unavailable("load memory", err)
	}
	record, err := memory.DecodeRecord(envelope)
	if err != nil {
		return retainedMemory{}, unavailable("decode retained memory", err)
	}
	return retainedMemory{record: record, payload: payload}, nil
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
	report := memory.ExportReport{}
	lastID := ""
	for {
		if err := ctx.Err(); err != nil {
			return exportUsage(report, bounded), err
		}
		nextID, err := p.nextExportID(ctx, lastID)
		if errors.Is(err, sql.ErrNoRows) {
			report = exportUsage(report, bounded)
			report.Completed = true
			return report, nil
		}
		if err != nil {
			return exportUsage(report, bounded), err
		}
		usedRecords, usedBytes := bounded.Usage()
		if usedRecords == request.MaxRecords {
			return exportUsage(report, bounded), memory.ErrTransferLimit
		}
		retained, envelopeBytes, payloadBytes, err := p.loadExportRecord(ctx, nextID)
		if err != nil {
			return exportUsage(report, bounded), err
		}
		if envelopeBytes+payloadBytes > request.MaxBytes-usedBytes {
			return exportUsage(report, bounded), memory.ErrTransferLimit
		}
		if payloadBytes > 0 {
			retained.payload, err = p.loadExportPayload(ctx, nextID, retained.record.Revision)
			if err != nil {
				return exportUsage(report, bounded), err
			}
			if uint64(len(retained.payload)) != payloadBytes {
				return exportUsage(report, bounded), unavailable("export payload changed during read", memory.ErrConflict)
			}
		}
		transfer := memory.Transfer{Record: retained.record}
		var consumed *countingReader
		if retained.record.Content.Transport == memory.TransportStream {
			consumed = &countingReader{reader: memory.Reader(bytes.NewReader(retained.payload))}
			transfer.Payload = consumed
		}
		if err := bounded.Put(ctx, transfer); err != nil {
			return exportUsage(report, bounded), err
		}
		if consumed != nil && consumed.bytes != uint64(len(retained.payload)) {
			return exportUsage(report, bounded), fmt.Errorf("%w: export sink did not consume stream payload", memory.ErrInvalidResult)
		}
		lastID = retained.record.ID
	}
}

func (p *Provider) nextExportID(ctx context.Context, afterID string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `SELECT memory_id FROM memory_provider_records WHERE doll_id=? AND memory_id>? ORDER BY memory_id LIMIT 1`, p.dollID, afterID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", sql.ErrNoRows
		}
		return "", operationError(ctx, "iterate export", err)
	}
	return id, nil
}

func (p *Provider) loadExportRecord(ctx context.Context, id string) (retainedMemory, uint64, uint64, error) {
	var envelope []byte
	var payloadBytes uint64
	err := p.db.QueryRowContext(ctx, `SELECT envelope, length(COALESCE(payload,x'')) FROM memory_provider_records WHERE doll_id=? AND memory_id=?`, p.dollID, id).Scan(&envelope, &payloadBytes)
	if err != nil {
		return retainedMemory{}, 0, 0, operationError(ctx, "export", err)
	}
	record, err := memory.DecodeRecord(envelope)
	if err != nil {
		return retainedMemory{}, 0, 0, unavailable("decode export", err)
	}
	return retainedMemory{record: record}, uint64(len(envelope)), payloadBytes, nil
}

func (p *Provider) loadExportPayload(ctx context.Context, id string, expectedRevision uint64) ([]byte, error) {
	var payload []byte
	var revision uint64
	if err := p.db.QueryRowContext(ctx, `SELECT revision,payload FROM memory_provider_records WHERE doll_id=? AND memory_id=?`, p.dollID, id).Scan(&revision, &payload); err != nil {
		return nil, operationError(ctx, "export payload", err)
	}
	if revision != expectedRevision {
		return nil, unavailable("export record changed during read", memory.ErrConflict)
	}
	return payload, nil
}

type countingReader struct {
	reader memory.StreamReader
	bytes  uint64
}

func (r *countingReader) ReadContext(ctx context.Context, buffer []byte) (int, error) {
	n, err := r.reader.ReadContext(ctx, buffer)
	r.bytes += uint64(n)
	return n, err
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
	transfers := make([]importCandidate, 0)
	for {
		transfer, err := bounded.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.Records, report.Bytes = bounded.Usage()
			return report, err
		}
		payload, reason, prepareErr := p.prepareContent(ctx, transfer.Record, transfer.Payload)
		if prepareErr != nil {
			report.Records, report.Bytes = bounded.Usage()
			return report, prepareErr
		}
		if reason != "" {
			if transfer.Payload != nil {
				if _, drainErr := memory.CopyBounded(ctx, memory.Writer(io.Discard), transfer.Payload, request.MaxBytes); drainErr != nil {
					report.Records, report.Bytes = bounded.Usage()
					return report, drainErr
				}
			}
			report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: transfer.Record.Reference(), Disposition: memory.ImportRejected, Reason: reason})
		}
		transfers = append(transfers, importCandidate{retained: retainedMemory{record: transfer.Record, payload: payload}, rejection: reason})
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
	for _, candidate := range transfers {
		retained := candidate.retained
		record := retained.record
		if candidate.rejection != "" {
			continue
		}
		existing, loadErr := p.loadTx(ctx, tx, record.ID)
		if loadErr == nil {
			disposition, compareErr := compareRetained(existing, retained)
			if compareErr != nil {
				report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Disposition: memory.ImportRejected, Reason: compareErr.Error()})
				continue
			}
			if disposition == memory.ImportIdempotent {
				ref := existing.record.Reference()
				report.Outcomes = append(report.Outcomes, memory.ImportOutcome{Source: record.Reference(), Retained: &ref, Disposition: memory.ImportIdempotent})
				continue
			}
		} else if !errors.Is(loadErr, sql.ErrNoRows) {
			return rollback(loadErr)
		}
		result, rememberErr := p.rememberTx(ctx, tx, retained, "")
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
		MaxStreamBytes:  p.maxStreamBytes,
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

func (p *Provider) prepareContent(ctx context.Context, record memory.Record, source memory.StreamReader) ([]byte, string, error) {
	mediaType, _, err := mime.ParseMediaType(record.Content.MediaType)
	if err != nil {
		return nil, "invalid content media type", nil
	}
	if mediaType != "text/plain" && mediaType != "application/json" {
		return nil, "unsupported content type: " + mediaType, nil
	}
	var payload []byte
	switch record.Content.Transport {
	case memory.TransportInline:
		if uint64(len(record.Content.Data)) > p.maxInlineBytes {
			return nil, "inline content exceeds provider limit", nil
		}
	case memory.TransportStream:
		if record.Content.Size != nil && uint64(*record.Content.Size) > p.maxStreamBytes {
			return nil, "stream content exceeds provider limit", nil
		}
		var buffer bytes.Buffer
		if _, err := memory.CopyBounded(ctx, memory.Writer(&buffer), source, p.maxStreamBytes); err != nil {
			if errors.Is(err, memory.ErrTransferLimit) {
				return nil, "stream content exceeds provider limit", nil
			}
			return nil, "", err
		}
		payload = append([]byte(nil), buffer.Bytes()...)
		if record.Content.Size != nil && int64(len(payload)) != *record.Content.Size {
			return nil, "stream content size does not match declaration", nil
		}
	default:
		return nil, "unsupported content transport", nil
	}
	content := record.Content.Data
	if record.Content.Transport == memory.TransportStream {
		content = payload
	}
	if mediaType == "text/plain" && !utf8.Valid(content) {
		return nil, "streamed text is not valid UTF-8", nil
	}
	if mediaType == "application/json" && !json.Valid(content) {
		return nil, "streamed JSON is invalid", nil
	}
	return payload, "", nil
}

func searchableText(record memory.Record, payload []byte) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(record.Content.MediaType)
	if record.Content.Transport == memory.TransportStream {
		return string(payload), nil
	}
	if mediaType == "text/plain" {
		var text string
		if err := json.Unmarshal(record.Content.Data, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	return string(record.Content.Data), nil
}

func retainedDigest(envelope, payload []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(envelope)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func legacyEnvelopeDigest(envelope []byte) string {
	digest := sha256.Sum256(envelope)
	return hex.EncodeToString(digest[:])
}

func compareRetained(existing, incoming retainedMemory) (memory.ImportDisposition, error) {
	disposition, err := memory.CompareImportRevision(existing.record, incoming.record)
	if err != nil {
		return disposition, err
	}
	if disposition == memory.ImportIdempotent && !bytes.Equal(existing.payload, incoming.payload) {
		return memory.ImportRejected, fmt.Errorf("%w: same revision has different stream payload", memory.ErrConflict)
	}
	return disposition, nil
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
