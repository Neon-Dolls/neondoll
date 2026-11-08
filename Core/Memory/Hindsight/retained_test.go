package hindsight

import (
	"bytes"
	"context"
	"strings"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestMatchingTextWithNativeAttributeChangesIsTransformed(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*fakeHindsight)
		want      string
	}{
		{name: "context", configure: func(fake *fakeHindsight) { fake.nextContext = "changed context" }, want: "changed context"},
		{name: "tags", configure: func(fake *fakeHindsight) { fake.nextTags = []string{"changed-tag"} }, want: "changed-tag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHindsight("")
			defer fake.close()
			test.configure(fake)
			provider := openTestProvider(t, fake, "doll")
			result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: testRecord("memory", 1, "same text")})
			if err != nil || result.Outcome != memory.RememberTransformed || !strings.Contains(string(result.Retained.Content.Data), test.want) {
				t.Fatalf("Remember = %#v, %v", result, err)
			}
		})
	}
}

func TestRememberAndExportShareNativeRetainedRepresentation(t *testing.T) {
	for _, test := range []struct {
		name, content string
		zero          bool
	}{
		{name: "multiple", content: "transform: source"},
		{name: "zero", content: "source", zero: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHindsight("")
			defer fake.close()
			fake.zeroUnits = test.zero
			provider := openTestProvider(t, fake, "doll")
			result, err := provider.Remember(context.Background(), memory.RememberRequest{Record: testRecord("memory", 1, test.content)})
			if err != nil || result.Outcome != memory.RememberTransformed {
				t.Fatalf("Remember = %#v, %v", result, err)
			}
			sink := &collectSink{}
			report, err := provider.Export(context.Background(), memory.ExportRequest{MaxRecords: 2, MaxBytes: 1 << 20}, sink)
			if err != nil || !report.Completed || len(sink.transfers) != 1 {
				t.Fatalf("Export = %#v, %v", report, err)
			}
			if !bytes.Equal(result.Retained.Content.Data, sink.transfers[0].Record.Content.Data) {
				t.Fatalf("Remember and Export retained views differ")
			}
			if strings.Contains(string(sink.transfers[0].Record.Content.Data), stateKey) {
				t.Fatal("internal replay state leaked into export")
			}
		})
	}
}
