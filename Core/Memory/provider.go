package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	Payload io.Reader
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
	Payload   io.Reader
	ReplayKey string
}

type RememberResult struct {
	Outcome  RememberOutcome
	Retained *Record
	Reason   string
}

func (r RememberResult) Validate() error {
	switch r.Outcome {
	case RememberStored, RememberTransformed:
		if r.Retained == nil {
			return fmt.Errorf("%w: retained record required", ErrInvalidResult)
		}
		return r.Retained.Validate()
	case RememberRejected:
		if r.Retained != nil || r.Reason == "" {
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

type Capabilities struct {
	ContractVersion       int
	ContentTypes          []string
	MaxInlineBytes        uint64
	MaxStreamBytes        uint64
	SupportsUpdates       bool
	SupportsConsolidation bool
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

// CompareImportRevision defines the portable conflict rule. Providers may
// transform a newer revision, but equal conflicting and stale revisions are
// rejected; exact equal replay is idempotent.
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
func CopyBounded(ctx context.Context, dst io.Writer, src io.Reader, maxBytes uint64) (uint64, error) {
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
			n, err := src.Read(probe[:])
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
		n, readErr := src.Read(chunk)
		if n > 0 {
			written, writeErr := dst.Write(chunk[:n])
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
