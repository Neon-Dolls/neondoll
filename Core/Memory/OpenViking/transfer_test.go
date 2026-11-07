package openviking

import (
	"bytes"
	"context"
	"errors"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestStreamExportImportAndIsolation(t *testing.T) {
	f, server := newFake(t)
	source := openTest(t, server.URL, "a")
	f.mu.Lock()
	f.docs[source.root+"/.relations.json"] = `{"provider_native":true}`
	f.mu.Unlock()
	payload := []byte(`{"snowman":"☃"}`)
	size := int64(len(payload))
	r := record("stream", 1, nil)
	r.Content = memory.Content{MediaType: "application/json", Transport: memory.TransportStream, Size: &size}
	if _, err := source.Remember(context.Background(), memory.RememberRequest{Record: r, Payload: memory.Reader(bytes.NewReader(payload))}); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	report, err := source.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink)
	if err != nil || !report.Completed || len(sink.transfers) != 1 || !bytes.Equal(sink.payloads[0], payload) {
		t.Fatalf("export: %#v %v", report, err)
	}
	target := openTest(t, server.URL, "b")
	imported, err := target.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &sliceSource{transfers: sink.transfers, payloads: sink.payloads})
	if err != nil || !imported.Completed {
		t.Fatalf("import: %#v %v", imported, err)
	}
	for _, p := range []*Provider{source, target} {
		got, err := p.Recall(context.Background(), memory.RecallRequest{Query: "snowman", Limit: 10})
		if err != nil || len(got.Matches) != 1 {
			t.Fatalf("isolated recall: %#v %v", got, err)
		}
	}
}

func TestImportOperationalFailureStopsWithPartialReport(t *testing.T) {
	f, server := newFake(t)
	p := openTest(t, server.URL, "a")
	f.mu.Lock()
	f.failWritesAfter = 1
	f.mu.Unlock()
	source := &sliceSource{transfers: []memory.Transfer{{Record: record("one", 1, "one")}, {Record: record("two", 1, "two")}, {Record: record("three", 1, "three")}}}
	report, err := p.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, source)
	if err == nil || report.Completed || len(report.Outcomes) != 1 || source.i != 2 {
		t.Fatalf("operational failure: %#v err=%v consumed=%d", report, err, source.i)
	}
	if report.Records != 2 {
		t.Fatalf("attempted usage not preserved: %#v", report)
	}
}

func TestImportAuthenticationCancellationAndRecordConflict(t *testing.T) {
	_, server := newFake(t)
	wrong, err := Open(Config{Endpoint: server.URL, APIKey: "wrong", UserID: "u", DollID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := wrong.Import(context.Background(), memory.ImportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, &sliceSource{transfers: []memory.Transfer{{Record: record("m", 1, "x")}}})
	if err == nil || report.Completed {
		t.Fatalf("authentication became rejection: %#v %v", report, err)
	}
	p := openTest(t, server.URL, "ok")
	if _, err = p.Remember(context.Background(), memory.RememberRequest{Record: record("m", 2, "new")}); err != nil {
		t.Fatal(err)
	}
	report, err = p.Import(context.Background(), memory.ImportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, &sliceSource{transfers: []memory.Transfer{{Record: record("m", 1, "old")}}})
	if err != nil || !report.Completed || len(report.Outcomes) != 1 || report.Outcomes[0].Disposition != memory.ImportRejected {
		t.Fatalf("record conflict: %#v %v", report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err = p.Import(ctx, memory.ImportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, &sliceSource{})
	if !errors.Is(err, context.Canceled) || report.Completed {
		t.Fatalf("cancellation: %#v %v", report, err)
	}
}

func TestImportMalformedUpstreamProtocolTerminates(t *testing.T) {
	f, server := newFake(t)
	p := openTest(t, server.URL, "d")
	f.mu.Lock()
	f.malformed = true
	f.mu.Unlock()
	report, err := p.Import(context.Background(), memory.ImportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, &sliceSource{transfers: []memory.Transfer{{Record: record("m", 1, "x")}}})
	if !errors.Is(err, memory.ErrInvalidResult) || report.Completed || report.Records != 1 {
		t.Fatalf("malformed protocol became rejection: %#v %v", report, err)
	}
}
