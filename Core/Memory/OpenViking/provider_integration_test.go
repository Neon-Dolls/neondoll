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
	p, err := Open(Config{Endpoint: endpoint, APIKey: os.Getenv("OPENVIKING_TEST_API_KEY"), UserID: userID, DollID: dollID, Namespace: "neondoll-integration", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if health, err := p.Health(ctx); err != nil || health.Status != memory.HealthHealthy {
		t.Fatalf("health: %#v %v", health, err)
	}
	rec := record("integration-"+time.Now().UTC().Format("20060102T150405.000000000"), 1, "OpenViking live integration")
	if got, err := p.Remember(ctx, memory.RememberRequest{Record: rec, ReplayKey: rec.ID}); err != nil || got.Outcome == memory.RememberRejected {
		t.Fatalf("remember: %#v %v", got, err)
	}
}
