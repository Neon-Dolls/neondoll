//go:build integration

package hindsight

import (
	"context"
	"os"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestLiveHindsightRoundTrip(t *testing.T) {
	endpoint := os.Getenv("HINDSIGHT_TEST_ENDPOINT")
	dollID := os.Getenv("HINDSIGHT_TEST_DOLL_ID")
	if endpoint == "" || dollID == "" {
		t.Skip("live Hindsight configuration is not set")
	}
	provider, err := Open(Config{Endpoint: endpoint, Token: os.Getenv("HINDSIGHT_TEST_TOKEN"), DollID: dollID})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := provider.Health(ctx); err != nil {
		t.Fatal(err)
	}
	record := testRecord("live-memory", 1, "NeonDoll Hindsight integration memory")
	if _, err := provider.Remember(ctx, memory.RememberRequest{Record: record, ReplayKey: "live-request"}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Recall(ctx, memory.RecallRequest{Query: "NeonDoll integration", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	if report, err := provider.Export(ctx, memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink); err != nil || !report.Completed {
		t.Fatalf("export = %#v, %v", report, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{Endpoint: endpoint, Token: os.Getenv("HINDSIGHT_TEST_TOKEN"), DollID: dollID})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Health(ctx); err != nil {
		t.Fatal(err)
	}
}
