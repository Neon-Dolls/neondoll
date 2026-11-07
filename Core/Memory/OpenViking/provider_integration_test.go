//go:build integration

package openviking

import (
	"context"
	"os"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestLiveOpenVikingHealthAndRoundTrip(t *testing.T) {
	endpoint := os.Getenv("OPENVIKING_TEST_ENDPOINT")
	userID := os.Getenv("OPENVIKING_TEST_USER_ID")
	dollID := os.Getenv("OPENVIKING_TEST_DOLL_ID")
	if endpoint == "" || userID == "" || dollID == "" {
		t.Skip("live OpenViking configuration is not set")
	}
	cfg := Config{Endpoint: endpoint, APIKey: os.Getenv("OPENVIKING_TEST_API_KEY"), UserID: userID, DollID: dollID, Namespace: "neondoll-integration", Timeout: 30 * time.Second}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if health, err := p.Health(ctx); err != nil || health.Status != memory.HealthHealthy {
		t.Fatalf("health: %#v %v", health, err)
	}
	rec := record("integration-"+time.Now().UTC().Format("20060102T150405.000000000"), 1, "OpenViking live integration "+time.Now().UTC().String())
	if got, err := p.Remember(ctx, memory.RememberRequest{Record: rec, ReplayKey: rec.ID}); err != nil || got.Outcome == memory.RememberRejected {
		t.Fatalf("remember: %#v %v", got, err)
	}
	sink := &collectSink{}
	if report, err := p.Export(ctx, memory.ExportRequest{MaxRecords: 1000, MaxBytes: 64 << 20}, sink); err != nil || !report.Completed || !containsTransfer(sink.transfers, rec.ID) {
		t.Fatalf("export/read: %#v %v", report, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got, err := p.Remember(ctx, memory.RememberRequest{Record: rec, ReplayKey: rec.ID}); err != nil || got.Retained == nil || got.Retained.ID != rec.ID {
		t.Fatalf("reopen/replay: %#v %v", got, err)
	}
	for {
		got, err := p.Recall(ctx, memory.RecallRequest{Query: "OpenViking live integration", Limit: 100})
		if err == nil && containsMatch(got.Matches, rec.ID) {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("eventual recall: %#v %v", got, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func containsTransfer(transfers []memory.Transfer, id string) bool {
	for _, transfer := range transfers {
		if transfer.Record.ID == id {
			return true
		}
	}
	return false
}
func containsMatch(matches []memory.RecallMatch, id string) bool {
	for _, match := range matches {
		if match.Reference.ID == id {
			return true
		}
	}
	return false
}
