package hindsight

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type listDocuments struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func (p *Provider) Export(ctx context.Context, req memory.ExportRequest, sink memory.ExportSink) (memory.ExportReport, error) {
	bounded, err := memory.NewBoundedExportSink(req, sink)
	if err != nil {
		return memory.ExportReport{}, err
	}
	report := memory.ExportReport{}
	query := url.Values{"tags": {"neondoll"}, "tags_match": {"all_strict"}, "limit": {strconv.Itoa(p.pageSize)}, "offset": {"0"}}
	for {
		var page listDocuments
		if err := p.call(ctx, http.MethodGet, p.bankPath()+"/documents", query, nil, &page); err != nil {
			return exportFailed(report, bounded, err)
		}
		for _, item := range page.Items {
			doc, found, err := p.getDocument(ctx, item.ID)
			if err != nil {
				return exportFailed(report, bounded, err)
			}
			if !found {
				return exportFailed(report, bounded, fmt.Errorf("%w: listed document disappeared", memory.ErrUnavailable))
			}
			state, err := decodeState(doc)
			if err != nil {
				return exportFailed(report, bounded, err)
			}
			units, err := p.listDocumentMemories(ctx, doc.ID)
			if err != nil {
				return exportFailed(report, bounded, err)
			}
			retained, err := retainedRecord(state.Record, units)
			if err != nil {
				return exportFailed(report, bounded, err)
			}
			if err := bounded.Put(ctx, memory.Transfer{Record: retained}); err != nil {
				report.Failures = append(report.Failures, memory.TransferFailure{Reference: retained.Reference(), Reason: err.Error()})
				return exportFailed(report, bounded, err)
			}
		}
		if page.Offset+len(page.Items) >= page.Total || len(page.Items) == 0 {
			break
		}
		query.Set("offset", strconv.Itoa(page.Offset+len(page.Items)))
	}
	report.Completed = true
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func exportFailed(report memory.ExportReport, bounded *memory.BoundedExportSink, err error) (memory.ExportReport, error) {
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
			return importFailed(report, bounded, err)
		}
		result, rememberErr := p.Remember(ctx, memory.RememberRequest{Record: transfer.Record, Payload: transfer.Payload})
		outcome := memory.ImportOutcome{Source: transfer.Record.Reference()}
		if rememberErr != nil {
			if errors.Is(rememberErr, memory.ErrConflict) {
				outcome.Disposition, outcome.Reason = memory.ImportRejected, rememberErr.Error()
				report.Outcomes = append(report.Outcomes, outcome)
				continue
			}
			return importFailed(report, bounded, rememberErr)
		}
		if result.Outcome == memory.RememberRejected {
			outcome.Disposition, outcome.Reason = memory.ImportRejected, result.Reason
		} else {
			retained := result.Retained.Reference()
			outcome.Retained = &retained
			if result.Outcome == memory.RememberTransformed {
				outcome.Disposition = memory.ImportTransformed
			} else {
				outcome.Disposition = memory.ImportStored
			}
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func importFailed(report memory.ImportReport, bounded *memory.BoundedImportSource, err error) (memory.ImportReport, error) {
	report.Completed = false
	report.Records, report.Bytes = bounded.Usage()
	return report, err
}
