package memory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
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

type consumingSink struct {
	payload   bytes.Buffer
	transfers int
}

func (s *consumingSink) Put(ctx context.Context, transfer Transfer) error {
	s.transfers++
	if transfer.Payload == nil {
		return nil
	}
	_, err := CopyBounded(ctx, Writer(&s.payload), transfer.Payload, 1<<20)
	return err
}

func TestRememberOutcomesAndRecallBounds(t *testing.T) {
	r := fixtureRecord()
	transformation := &Transformation{Kind: "content_conversion", Description: "provider extracted text from image", FromMediaType: "image/png", ToMediaType: "text/plain"}
	for _, result := range []RememberResult{{Outcome: RememberStored, Retained: &r}, {Outcome: RememberTransformed, Retained: &r, Transformation: transformation}, {Outcome: RememberRejected, Reason: "unsupported image encoding"}} {
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (RememberResult{Outcome: RememberRejected}).Validate(); err == nil {
		t.Fatal("accepted reasonless rejection")
	}
	if err := (RememberResult{Outcome: RememberTransformed, Retained: &r}).Validate(); err == nil {
		t.Fatal("accepted transformation without explicit detail")
	}
	if err := (RememberResult{Outcome: RememberRejected, Retained: &r, Reason: "unsupported"}).Validate(); err == nil {
		t.Fatal("accepted rejected content with retained representation")
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
	if n, err := CopyBounded(context.Background(), Writer(&out), Reader(strings.NewReader("1234")), 4); err != nil || n != 4 || out.String() != "1234" {
		t.Fatalf("bounded copy: %d %q %v", n, out.String(), err)
	}
	out.Reset()
	if _, err := CopyBounded(context.Background(), Writer(&out), Reader(strings.NewReader("12345")), 4); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CopyBounded(ctx, Writer(io.Discard), Reader(strings.NewReader("x")), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCopyBoundedCancelsBlockedIO(t *testing.T) {
	started := make(chan struct{})
	reader := StreamReaderFunc(func(ctx context.Context, _ []byte) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := CopyBounded(ctx, Writer(io.Discard), reader, 10); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked stream did not honor cancellation contract")
	}
}

func TestCopyBoundedCancelsBlockedWriter(t *testing.T) {
	started := make(chan struct{})
	writer := StreamWriterFunc(func(ctx context.Context, _ []byte) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := CopyBounded(ctx, writer, Reader(strings.NewReader("x")), 10); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked writer cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer did not honor cancellation contract")
	}
}

func TestBoundedExportSinkExactBoundaryAndOverflow(t *testing.T) {
	record := fixtureRecord()
	envelope, _ := EncodeRecord(record)
	downstream := &consumingSink{}
	sink, err := NewBoundedExportSink(ExportRequest{MaxRecords: 1, MaxBytes: uint64(len(envelope))}, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Put(context.Background(), Transfer{Record: record}); err != nil {
		t.Fatalf("exact boundary: %v", err)
	}
	if err := sink.Put(context.Background(), Transfer{Record: record}); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("record overflow: %v", err)
	}
	records, used := sink.Usage()
	if records != 1 || used != uint64(len(envelope)) {
		t.Fatalf("usage: %d %d", records, used)
	}
}

func TestBoundedTransferStreamBytesAndCancellation(t *testing.T) {
	record := fixtureRecord()
	record.Content = Content{MediaType: "image/png", Transport: TransportStream}
	envelope, _ := EncodeRecord(record)
	downstream := &consumingSink{}
	sink, err := NewBoundedExportSink(ExportRequest{MaxRecords: 1, MaxBytes: uint64(len(envelope) + 4)}, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Put(context.Background(), Transfer{Record: record, Payload: Reader(strings.NewReader("1234"))}); err != nil {
		t.Fatalf("exact stream boundary: %v", err)
	}
	if downstream.payload.String() != "1234" {
		t.Fatalf("payload: %q", downstream.payload.String())
	}

	overflowSink, _ := NewBoundedExportSink(ExportRequest{MaxRecords: 1, MaxBytes: uint64(len(envelope) + 4)}, &consumingSink{})
	if err := overflowSink.Put(context.Background(), Transfer{Record: record, Payload: Reader(strings.NewReader("12345"))}); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("stream overflow: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := overflowSink.Put(ctx, Transfer{Record: record, Payload: Reader(strings.NewReader("x"))}); !errors.Is(err, context.Canceled) {
		t.Fatalf("sink cancellation: %v", err)
	}
}

func TestBoundedImportSourceLimits(t *testing.T) {
	record := fixtureRecord()
	envelope, _ := EncodeRecord(record)
	source, err := NewBoundedImportSource(ImportRequest{MaxRecords: 1, MaxBytes: uint64(len(envelope))}, &sliceSource{records: []Transfer{{Record: record}, {Record: record}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next(context.Background()); err != nil {
		t.Fatalf("exact boundary: %v", err)
	}
	if _, err := source.Next(context.Background()); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("record overflow: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("source cancellation: %v", err)
	}
}

func TestBoundedImportSourceStreamOverflow(t *testing.T) {
	record := fixtureRecord()
	record.Content = Content{MediaType: "application/octet-stream", Transport: TransportStream}
	envelope, _ := EncodeRecord(record)
	source, err := NewBoundedImportSource(ImportRequest{MaxRecords: 1, MaxBytes: uint64(len(envelope) + 2)}, &sliceSource{records: []Transfer{{Record: record, Payload: Reader(strings.NewReader("123"))}}})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := source.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CopyBounded(context.Background(), Writer(io.Discard), transfer.Payload, 100); !errors.Is(err, ErrTransferLimit) {
		t.Fatalf("import stream overflow: %v", err)
	}
}
