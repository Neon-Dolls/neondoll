package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const copyBufferSize = 32 * 1024

const (
	ContractVersion = 1
	MaxRecallLimit  = 1000
)

var (
	ErrInvalidRequest     = errors.New("memory: invalid request")
	ErrInvalidResult      = errors.New("memory: invalid result")
	ErrUnavailable        = errors.New("memory: provider unavailable")
	ErrUnsupportedContent = errors.New("memory: unsupported content")
	ErrConflict           = errors.New("memory: revision conflict")
	ErrTransferLimit      = errors.New("memory: transfer limit exceeded")
)

type RememberOutcome string

const (
	RememberStored      RememberOutcome = "stored"
	RememberTransformed RememberOutcome = "transformed"
	RememberRejected    RememberOutcome = "rejected"
)

type Transfer struct {
	Record  Record
	Payload StreamReader
}

// StreamReader and StreamWriter make cancellation part of the transfer
// contract, including while an operation is blocked. Implementations must
// return promptly with ctx.Err() when ctx is cancelled.
type StreamReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type StreamWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

type StreamReaderFunc func(context.Context, []byte) (int, error)

func (f StreamReaderFunc) ReadContext(ctx context.Context, p []byte) (int, error) {
	return f(ctx, p)
}

type StreamWriterFunc func(context.Context, []byte) (int, error)

func (f StreamWriterFunc) WriteContext(ctx context.Context, p []byte) (int, error) {
	return f(ctx, p)
}

// Reader adapts an immediately readable io.Reader. It checks cancellation
// before calling Read; callers must not use it for readers that may block.
func Reader(reader io.Reader) StreamReader {
	return StreamReaderFunc(func(ctx context.Context, p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return reader.Read(p)
	})
}

// Writer adapts an immediately writable io.Writer. It checks cancellation
// before calling Write; callers must not use it for writers that may block.
func Writer(writer io.Writer) StreamWriter {
	return StreamWriterFunc(func(ctx context.Context, p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return writer.Write(p)
	})
}

func (t Transfer) Validate() error {
	if err := t.Record.Validate(); err != nil {
		return err
	}
	if (t.Record.Content.Transport == TransportStream) != (t.Payload != nil) {
		return fmt.Errorf("%w: payload does not match transport", ErrInvalidRequest)
	}
	return nil
}

type RememberRequest struct {
	Record    Record
	Payload   StreamReader
	ReplayKey string
}

type Transformation struct {
	Kind          string
	Description   string
	FromMediaType string
	ToMediaType   string
}

type RememberResult struct {
	Outcome        RememberOutcome
	Retained       *Record
	Reason         string
	Transformation *Transformation
}

func (r RememberResult) Validate() error {
	switch r.Outcome {
	case RememberStored:
		if r.Retained == nil {
			return fmt.Errorf("%w: retained record required", ErrInvalidResult)
		}
		if r.Transformation != nil {
			return fmt.Errorf("%w: stored outcome cannot report transformation", ErrInvalidResult)
		}
		return r.Retained.Validate()
	case RememberTransformed:
		if r.Retained == nil || r.Transformation == nil || strings.TrimSpace(r.Transformation.Kind) == "" || strings.TrimSpace(r.Transformation.Description) == "" {
			return fmt.Errorf("%w: transformed outcome requires retained representation and transformation detail", ErrInvalidResult)
		}
		return r.Retained.Validate()
	case RememberRejected:
		if r.Retained != nil || r.Transformation != nil || r.Reason == "" {
			return fmt.Errorf("%w: rejection reason required", ErrInvalidResult)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown remember outcome", ErrInvalidResult)
	}
}

type RecallRequest struct {
	Query       string
	Limit       uint32
	Kinds       []string
	SourceTypes []string
}

func (r RecallRequest) Validate() error {
	if r.Limit == 0 || r.Limit > MaxRecallLimit {
		return ErrInvalidRequest
	}
	return nil
}

type RecallMatch struct {
	Reference Reference
	Record    *Record
}

type RecallResult struct{ Matches []RecallMatch }

func (r RecallResult) Validate(request RecallRequest) error {
	if err := request.Validate(); err != nil || len(r.Matches) > int(request.Limit) {
		return ErrInvalidResult
	}
	for _, match := range r.Matches {
		if match.Reference.ID == "" || match.Reference.Revision == 0 {
			return ErrInvalidResult
		}
		if match.Record != nil {
			if err := match.Record.Validate(); err != nil || match.Record.Reference() != match.Reference {
				return ErrInvalidResult
			}
		}
	}
	return nil
}

type ExportRequest struct {
	MaxRecords uint64
	MaxBytes   uint64
}
type ExportSink interface {
	Put(context.Context, Transfer) error
}
type ExportReport struct {
	Records   uint64
	Bytes     uint64
	Completed bool
	Failures  []TransferFailure
}

type ImportRequest struct {
	MaxRecords uint64
	MaxBytes   uint64
}
type ImportSource interface {
	Next(context.Context) (Transfer, error)
}
type ImportDisposition string

const (
	ImportStored      ImportDisposition = "stored"
	ImportTransformed ImportDisposition = "transformed"
	ImportIdempotent  ImportDisposition = "idempotent"
	ImportRejected    ImportDisposition = "rejected"
)

type ImportOutcome struct {
	Source      Reference
	Retained    *Reference
	Disposition ImportDisposition
	Reason      string
}
type ImportReport struct {
	Records   uint64
	Bytes     uint64
	Completed bool
	Outcomes  []ImportOutcome
	Failures  []TransferFailure
}
type TransferFailure struct {
	Reference Reference
	Reason    string
}

func validateBounds(records, bytes uint64) error {
	if records == 0 || bytes == 0 {
		return ErrInvalidRequest
	}
	return nil
}

type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
)

type Health struct {
	Status HealthStatus
	Detail string
}

type Provider interface {
	Remember(context.Context, RememberRequest) (RememberResult, error)
	Recall(context.Context, RecallRequest) (RecallResult, error)
	Export(context.Context, ExportRequest, ExportSink) (ExportReport, error)
	Import(context.Context, ImportRequest, ImportSource) (ImportReport, error)
	Capabilities(context.Context) (Capabilities, error)
	Health(context.Context) (Health, error)
}

// CompareImportRevision is an optional deterministic policy for providers that
// advertise strict revision conflict handling. The Memory Provider contract
// does not require every backend to offer compare-and-swap semantics.
func CompareImportRevision(existing, incoming Record) (ImportDisposition, error) {
	if existing.ID != incoming.ID {
		return "", ErrInvalidRequest
	}
	if incoming.Revision < existing.Revision {
		return ImportRejected, fmt.Errorf("%w: stale revision", ErrConflict)
	}
	if incoming.Revision > existing.Revision {
		return ImportStored, nil
	}
	a, err := EncodeRecord(existing)
	if err != nil {
		return "", err
	}
	b, err := EncodeRecord(incoming)
	if err != nil {
		return "", err
	}
	if string(a) == string(b) {
		return ImportIdempotent, nil
	}
	return ImportRejected, fmt.Errorf("%w: same revision differs", ErrConflict)
}

func ValidateExportRequest(r ExportRequest) error { return validateBounds(r.MaxRecords, r.MaxBytes) }
func ValidateImportRequest(r ImportRequest) error { return validateBounds(r.MaxRecords, r.MaxBytes) }

// CopyBounded copies a stream while enforcing cancellation and a hard byte
// bound. Providers can use it for both import and export payloads without
// adopting a Core-owned storage representation.
func CopyBounded(ctx context.Context, dst StreamWriter, src StreamReader, maxBytes uint64) (uint64, error) {
	if maxBytes == 0 {
		return 0, ErrInvalidRequest
	}
	buffer := make([]byte, copyBufferSize)
	var total uint64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		remaining := maxBytes - total
		if remaining == 0 {
			var probe [1]byte
			n, err := src.ReadContext(ctx, probe[:])
			if n > 0 || err == nil {
				return total, ErrTransferLimit
			}
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
		chunk := buffer
		if uint64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := src.ReadContext(ctx, chunk)
		if n > 0 {
			written, writeErr := dst.WriteContext(ctx, chunk[:n])
			total += uint64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// TransferBudget accounts for complete encoded records, including streamed
// payload bytes. It can be shared by a bounded source or sink. Transfer users
// must consume a stream payload before requesting the next transfer.
type TransferBudget struct {
	mu         sync.Mutex
	maxRecords uint64
	maxBytes   uint64
	records    uint64
	bytes      uint64
}

func NewTransferBudget(maxRecords, maxBytes uint64) (*TransferBudget, error) {
	if err := validateBounds(maxRecords, maxBytes); err != nil {
		return nil, err
	}
	return &TransferBudget{maxRecords: maxRecords, maxBytes: maxBytes}, nil
}

func (b *TransferBudget) Usage() (records, bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.records, b.bytes
}

func (b *TransferBudget) hasRecordCapacity() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.records < b.maxRecords
}

func (b *TransferBudget) admit(transfer Transfer) (Transfer, error) {
	if err := transfer.Validate(); err != nil {
		return Transfer{}, err
	}
	envelope, err := EncodeRecord(transfer.Record)
	if err != nil {
		return Transfer{}, err
	}
	b.mu.Lock()
	if b.records == b.maxRecords || uint64(len(envelope)) > b.maxBytes-b.bytes {
		b.mu.Unlock()
		return Transfer{}, ErrTransferLimit
	}
	b.records++
	b.bytes += uint64(len(envelope))
	b.mu.Unlock()
	if transfer.Payload != nil {
		transfer.Payload = &budgetReader{reader: transfer.Payload, budget: b}
	}
	return transfer, nil
}

type budgetReader struct {
	reader StreamReader
	budget *TransferBudget
}

func (r *budgetReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.budget.mu.Lock()
	remaining := r.budget.maxBytes - r.budget.bytes
	if remaining == 0 {
		r.budget.mu.Unlock()
		var probe [1]byte
		n, err := r.reader.ReadContext(ctx, probe[:])
		if n > 0 || err == nil {
			return 0, ErrTransferLimit
		}
		return 0, err
	}
	if uint64(len(p)) > remaining {
		p = p[:remaining]
	}
	reserved := uint64(len(p))
	r.budget.bytes += reserved
	r.budget.mu.Unlock()
	n, err := r.reader.ReadContext(ctx, p)
	if n < 0 || n > len(p) {
		r.budget.mu.Lock()
		r.budget.bytes -= reserved
		r.budget.mu.Unlock()
		return 0, fmt.Errorf("%w: stream reader returned invalid byte count", ErrInvalidResult)
	}
	r.budget.mu.Lock()
	r.budget.bytes -= reserved - uint64(n)
	r.budget.mu.Unlock()
	return n, err
}

type BoundedExportSink struct {
	budget *TransferBudget
	sink   ExportSink
}

func NewBoundedExportSink(request ExportRequest, sink ExportSink) (*BoundedExportSink, error) {
	budget, err := NewTransferBudget(request.MaxRecords, request.MaxBytes)
	if err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedExportSink{budget: budget, sink: sink}, nil
}
func (s *BoundedExportSink) Put(ctx context.Context, transfer Transfer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bounded, err := s.budget.admit(transfer)
	if err != nil {
		return err
	}
	return s.sink.Put(ctx, bounded)
}

// Usage reports attempted-transfer accounting. Once a valid transfer is
// admitted, its record envelope and any payload bytes consumed by the
// downstream sink remain charged even if that sink returns an error. This
// fail-closed rule prevents retries through the same wrapper from exceeding
// the caller's requested bounds.
func (s *BoundedExportSink) Usage() (uint64, uint64) { return s.budget.Usage() }

type BoundedImportSource struct {
	budget *TransferBudget
	source ImportSource
}

func NewBoundedImportSource(request ImportRequest, source ImportSource) (*BoundedImportSource, error) {
	budget, err := NewTransferBudget(request.MaxRecords, request.MaxBytes)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedImportSource{budget: budget, source: source}, nil
}
func (s *BoundedImportSource) Next(ctx context.Context) (Transfer, error) {
	if err := ctx.Err(); err != nil {
		return Transfer{}, err
	}
	// ImportSource is a sequential pull contract. Check the record bound before
	// advancing it so a rejected request cannot consume an upstream record.
	if !s.budget.hasRecordCapacity() {
		return Transfer{}, ErrTransferLimit
	}
	transfer, err := s.source.Next(ctx)
	if err != nil {
		return Transfer{}, err
	}
	return s.budget.admit(transfer)
}
func (s *BoundedImportSource) Usage() (uint64, uint64) { return s.budget.Usage() }
