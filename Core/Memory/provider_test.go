package memory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

type fakeProvider struct {
	mu      sync.Mutex
	records []Record
}

func (f *fakeProvider) Remember(ctx context.Context, req RememberRequest) (RememberResult, error) {
	if err := ctx.Err(); err != nil {
		return RememberResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, req.Record)
	r := req.Record
	return RememberResult{Outcome: RememberStored, Retained: &r}, nil
}
func (f *fakeProvider) Recall(_ context.Context, req RecallRequest) (RecallResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := RecallResult{}
	for i := len(f.records) - 1; i >= 0 && len(out.Matches) < int(req.Limit); i-- {
		r := f.records[i]
		out.Matches = append(out.Matches, RecallMatch{Reference: r.Reference(), Record: &r})
	}
	return out, nil
}
func (f *fakeProvider) Export(ctx context.Context, req ExportRequest, sink ExportSink) (ExportReport, error) {
	if err := ValidateExportRequest(req); err != nil {
		return ExportReport{}, err
	}
	f.mu.Lock()
	records := append([]Record(nil), f.records...)
	f.mu.Unlock()
	report := ExportReport{}
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if report.Records == req.MaxRecords {
			return report, ErrTransferLimit
		}
		if err := sink.Put(ctx, Transfer{Record: r}); err != nil {
			return report, err
		}
		report.Records++
	}
	report.Completed = true
	return report, nil
}
func (f *fakeProvider) Import(ctx context.Context, req ImportRequest, source ImportSource) (ImportReport, error) {
	if err := ValidateImportRequest(req); err != nil {
		return ImportReport{}, err
	}
	report := ImportReport{}
	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		tr, err := source.Next(ctx)
		if errors.Is(err, io.EOF) {
			report.Completed = true
			return report, nil
		}
		if err != nil {
			return report, err
		}
		res, err := f.Remember(ctx, RememberRequest{Record: tr.Record, Payload: tr.Payload})
		if err != nil {
			return report, err
		}
		report.Records++
		report.Outcomes = append(report.Outcomes, ImportOutcome{Source: tr.Record.Reference(), Retained: func() *Reference { x := res.Retained.Reference(); return &x }(), Disposition: ImportStored})
	}
}
func (*fakeProvider) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{ContractVersion: 1, ContentTypes: []string{"text/plain"}}, nil
}
func (*fakeProvider) Health(context.Context) (Health, error) {
	return Health{Status: HealthHealthy}, nil
}

type sliceSource struct {
	records []Transfer
	n       int
}

func (s *sliceSource) Next(context.Context) (Transfer, error) {
	if s.n == len(s.records) {
		return Transfer{}, io.EOF
	}
	r := s.records[s.n]
	s.n++
	return r, nil
}

type collectSink struct{ records []Record }

func (s *collectSink) Put(_ context.Context, t Transfer) error {
	if err := t.Validate(); err != nil {
		return err
	}
	s.records = append(s.records, t.Record)
	return nil
}

func TestRememberOutcomesAndRecallBounds(t *testing.T) {
	r := fixtureRecord()
	for _, result := range []RememberResult{{Outcome: RememberStored, Retained: &r}, {Outcome: RememberTransformed, Retained: &r}, {Outcome: RememberRejected, Reason: "unsupported"}} {
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (RememberResult{Outcome: RememberRejected}).Validate(); err == nil {
		t.Fatal("accepted reasonless rejection")
	}
	if err := (RecallRequest{Limit: MaxRecallLimit + 1}).Validate(); err == nil {
		t.Fatal("accepted unbounded recall")
	}
}

func TestExportImportIntegrityAndCancellation(t *testing.T) {
	source := &fakeProvider{records: []Record{fixtureRecord()}}
	sink := &collectSink{}
	report, err := source.Export(context.Background(), ExportRequest{MaxRecords: 2, MaxBytes: 1024}, sink)
	if err != nil || !report.Completed {
		t.Fatalf("export %v %+v", err, report)
	}
	target := &fakeProvider{}
	ir, err := target.Import(context.Background(), ImportRequest{MaxRecords: 2, MaxBytes: 1024}, &sliceSource{records: []Transfer{{Record: sink.records[0]}}})
	if err != nil || !ir.Completed || target.records[0].ID != "mem-1" {
		t.Fatalf("import %v %+v", err, ir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Export(ctx, ExportRequest{MaxRecords: 2, MaxBytes: 1024}, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestImportRevisionRules(t *testing.T) {
	a := fixtureRecord()
	same := a
	if got, err := CompareImportRevision(a, same); err != nil || got != ImportIdempotent {
		t.Fatalf("%s %v", got, err)
	}
	changed := a
	changed.Content.Data = []byte(`"changed"`)
	if _, err := CompareImportRevision(a, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	newer := a
	newer.Revision = 2
	if got, err := CompareImportRevision(a, newer); err != nil || got != ImportStored {
		t.Fatalf("newer: %s %v", got, err)
	}
}

func TestUnsupportedContentIsExplicit(t *testing.T) {
	r := fixtureRecord()
	r.Content.MediaType = "application/octet-stream"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "requires stream") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestConfigurationSelection(t *testing.T) {
	c := Configuration{Active: "primary", Providers: map[string]ProviderReference{"primary": {InstanceID: "memory-1", ContractVersion: 1}, "archive": {InstanceID: "memory-2", ContractVersion: 1}}}
	got, ok, err := c.ResolveActive()
	if err != nil || !ok || got.InstanceID != "memory-1" {
		t.Fatalf("resolve %+v %v %v", got, ok, err)
	}
	c.Active = "missing"
	if err := c.Validate(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("unknown active: %v", err)
	}
}

func TestCopyBounded(t *testing.T) {
	var out bytes.Buffer
	if n, err := CopyBounded(context.Background(), &out, strings.NewReader("1234"), 4); err != nil || n != 4 || out.String() != "1234" {
		t.Fatalf("bounded copy: %d %q %v", n, out.String(), err)
	}
	out.Reset()
	if _, err := CopyBounded(context.Background(), &out, strings.NewReader("12345"), 4); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CopyBounded(ctx, io.Discard, strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
