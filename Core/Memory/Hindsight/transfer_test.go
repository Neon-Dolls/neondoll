package hindsight

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestStreamRememberExportImportAndBounds(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	sourceProvider := openTestProvider(t, fake, "source")
	record := testRecord("stream", 1, "unused")
	record.Content = memory.Content{MediaType: "application/json", Transport: memory.TransportStream}
	payload := []byte(`{"name":"Neon","values":[1,2,3]}`)
	size := int64(len(payload))
	record.Content.Size = &size
	result, err := sourceProvider.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewReader(payload))})
	if err != nil || result.Outcome != memory.RememberStored {
		t.Fatalf("stream remember = %#v, %v", result, err)
	}

	sink := &collectSink{}
	report, err := sourceProvider.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink)
	if err != nil || !report.Completed || report.Records != 1 || len(sink.transfers) != 1 {
		t.Fatalf("export = %#v, %v", report, err)
	}
	if !bytes.Equal(sink.payloads[0], payload) {
		t.Fatalf("payload = %q", sink.payloads[0])
	}

	target := openTestProvider(t, fake, "target")
	importReport, err := target.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &sliceSource{transfers: sink.transfers, payloads: sink.payloads})
	if err != nil || !importReport.Completed || len(importReport.Outcomes) != 1 {
		t.Fatalf("import = %#v, %v", importReport, err)
	}

	limited := &collectSink{}
	report, err = sourceProvider.Export(context.Background(), memory.ExportRequest{MaxRecords: 1, MaxBytes: 16}, limited)
	if !errors.Is(err, memory.ErrTransferLimit) || report.Completed {
		t.Fatalf("bounded export = %#v, %v", report, err)
	}
}

func TestImportOperationalFailureStopsWithPartialReport(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	source := &sliceSource{transfers: []memory.Transfer{{Record: testRecord("one", 1, "one")}, {Record: testRecord("two", 1, "two")}}}
	fake.failRetainAfter = 1
	report, err := provider.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, source)
	if err == nil || report.Completed || report.Records != 2 || len(report.Outcomes) != 1 {
		t.Fatalf("partial import = %#v, %v", report, err)
	}
	result, recallErr := provider.Recall(context.Background(), memory.RecallRequest{Query: "one", Limit: 10})
	if recallErr != nil || len(result.Matches) != 1 {
		t.Fatalf("retained prefix = %#v, %v", result, recallErr)
	}
}

func TestExportOperationalFailurePreservesPartialAccounting(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	for _, id := range []string{"one", "two"} {
		if _, err := provider.Remember(context.Background(), memory.RememberRequest{Record: testRecord(id, 1, id)}); err != nil {
			t.Fatal(err)
		}
	}
	sink := &collectSink{afterFirst: func() { fake.mu.Lock(); fake.failList = true; fake.mu.Unlock() }}
	report, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink)
	if err == nil || report.Completed || report.Records != 1 || len(sink.transfers) != 1 {
		t.Fatalf("partial export = %#v, %v", report, err)
	}
}

func TestCancellationAndMalformedStream(t *testing.T) {
	fake := newFakeHindsight("")
	defer fake.close()
	provider := openTestProvider(t, fake, "doll")
	record := testRecord("json", 1, "unused")
	record.Content = memory.Content{MediaType: "application/json", Transport: memory.TransportStream}
	result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: record, Payload: memory.Reader(bytes.NewBufferString("not json"))})
	if err != nil || result.Outcome != memory.RememberRejected {
		t.Fatalf("malformed JSON = %#v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Recall(ctx, memory.RecallRequest{Query: "x", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

type collectSink struct {
	mu         sync.Mutex
	transfers  []memory.Transfer
	payloads   [][]byte
	afterFirst func()
}

func (s *collectSink) Put(ctx context.Context, transfer memory.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var payload bytes.Buffer
	if transfer.Payload != nil {
		if _, err := memory.CopyBounded(ctx, memory.Writer(&payload), transfer.Payload, 1<<20); err != nil {
			return err
		}
	}
	transfer.Payload = nil
	s.transfers = append(s.transfers, transfer)
	s.payloads = append(s.payloads, payload.Bytes())
	if len(s.transfers) == 1 && s.afterFirst != nil {
		s.afterFirst()
	}
	return nil
}

type sliceSource struct {
	index      int
	transfers  []memory.Transfer
	payloads   [][]byte
	afterFirst func()
}

func (s *sliceSource) Next(context.Context) (memory.Transfer, error) {
	if s.index == len(s.transfers) {
		return memory.Transfer{}, io.EOF
	}
	transfer := s.transfers[s.index]
	if transfer.Record.Content.Transport == memory.TransportStream {
		transfer.Payload = memory.Reader(bytes.NewReader(s.payloads[s.index]))
	}
	s.index++
	if s.index == 1 && s.afterFirst != nil {
		s.afterFirst()
	}
	return transfer, nil
}
