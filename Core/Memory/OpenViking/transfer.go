package openviking

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type listEntry struct {
	URI   string `json:"uri"`
	IsDir bool   `json:"isDir"`
}

func (p *Provider) Export(ctx context.Context, req memory.ExportRequest, sink memory.ExportSink) (memory.ExportReport, error) {
	bounded, err := memory.NewBoundedExportSink(req, sink)
	if err != nil {
		return memory.ExportReport{}, err
	}
	report := memory.ExportReport{}
	for offset := 0; ; {
		var entries []listEntry
		query := url.Values{"uri": {p.root}, "offset": {fmt.Sprint(offset)}, "limit": {"100"}, "sort_by": {"name"}, "sort_order": {"asc"}}
		var more bool
		if err := p.call(ctx, http.MethodGet, "/api/v1/fs/ls", query, nil, &entries, &more); err != nil {
			if isNotFound(err) {
				return completeExport(report, bounded)
			}
			return incompleteExport(report, bounded, err)
		}
		for _, entry := range entries {
			if entry.IsDir || !p.isRecordURI(entry.URI) {
				continue
			}
			doc, ok, err := p.readDocument(ctx, entry.URI)
			if err != nil {
				return incompleteExport(report, bounded, err)
			}
			if !ok {
				continue
			}
			transfer := memory.Transfer{Record: doc.Record}
			if doc.Record.Content.Transport == memory.TransportStream {
				transfer.Payload = memory.Reader(bytes.NewReader(doc.payload()))
			}
			if err := bounded.Put(ctx, transfer); err != nil {
				report.Failures = append(report.Failures, memory.TransferFailure{Reference: doc.Record.Reference(), Reason: err.Error()})
				return incompleteExport(report, bounded, err)
			}
		}
		if !more || len(entries) == 0 {
			break
		}
		offset += len(entries)
	}
	return completeExport(report, bounded)
}

func completeExport(report memory.ExportReport, bounded *memory.BoundedExportSink) (memory.ExportReport, error) {
	report.Completed = true
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func incompleteExport(report memory.ExportReport, bounded *memory.BoundedExportSink, err error) (memory.ExportReport, error) {
	report.Completed = false
	report.Records, report.Bytes = bounded.Usage()
	return report, err
}

func (p *Provider) Import(ctx context.Context, req memory.ImportRequest, source memory.ImportSource) (memory.ImportReport, error) {
	bounded, err := memory.NewBoundedImportSource(req, source)
	if err != nil {
		return memory.ImportReport{}, err
	}
	report := memory.ImportReport{Completed: true}
	for {
		transfer, err := bounded.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return incompleteImport(report, bounded, err)
		}
		result, rememberErr := p.Remember(ctx, memory.RememberRequest{Record: transfer.Record, Payload: transfer.Payload})
		outcome := memory.ImportOutcome{Source: transfer.Record.Reference()}
		if rememberErr != nil {
			if errors.Is(rememberErr, memory.ErrConflict) {
				outcome.Disposition, outcome.Reason = memory.ImportRejected, rememberErr.Error()
				report.Outcomes = append(report.Outcomes, outcome)
				continue
			}
			return incompleteImport(report, bounded, rememberErr)
		}
		if result.Outcome == memory.RememberRejected {
			outcome.Disposition, outcome.Reason = memory.ImportRejected, result.Reason
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		retained := result.Retained.Reference()
		outcome.Retained = &retained
		if result.Outcome == memory.RememberTransformed {
			outcome.Disposition = memory.ImportTransformed
		} else {
			outcome.Disposition = memory.ImportStored
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func incompleteImport(report memory.ImportReport, bounded *memory.BoundedImportSource, err error) (memory.ImportReport, error) {
	report.Completed = false
	report.Records, report.Bytes = bounded.Usage()
	return report, err
}
