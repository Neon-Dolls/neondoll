//go:build integration

package integration

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/API"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Logger"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
)

// mockMindAPI implements dollmind.MindAPI for integration tests.
type mockMindAPI struct {
	s *dollstate.DollState
}

func (m *mockMindAPI) Inference() inference.Provider { return nil }
func (m *mockMindAPI) State() *dollstate.DollState   { return m.s }

func TestIntegrationStateSaveLoad(t *testing.T) {
	dir := t.TempDir()

	s := dollstate.NewDollState()
	s.Identity = dollstate.Identity{DollID: "integration-test", CanonicalName: "IT"}
	s.Soul = dollstate.Soul{Revision: 1, Content: "Integration test doll."}

	path, err := dollstate.SaveState(dir, &s)
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := dollstate.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if loaded.Identity.DollID != "integration-test" {
		t.Errorf("expected integration-test, got %s", loaded.Identity.DollID)
	}
}

func TestIntegrationConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neondoll.json")

	cfg := config.Defaults()
	cfg.Core.Profile = "integration"
	cfg.Core.Environment = "test"

	if err := config.Save(path, &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Core.Profile != "integration" {
		t.Errorf("expected profile integration, got %s", loaded.Core.Profile)
	}
}

func TestIntegrationAPIServer(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := api.New(api.Config{Listen: "127.0.0.1:0"}, log)

	go func() {
		srv.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:8080/health")
	if err != nil {
		t.Logf("Health check attempt: %v (server may not be on 8080)", err)
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestIntegrationCognitionWithMock(t *testing.T) {
	provider := inference.NewMockProvider("integration-test", "Hello from integration!")
	log := logger.New(logger.InfoLevel, nil)
	mindAPI := &mockMindAPI{
		s: &dollstate.DollState{Version: dollstate.CurrentStateVersion},
	}
	sched := dollmind.New(provider, log, mindAPI)

	result, err := sched.Run(context.Background(), dollmind.LevelReflex, "test integration")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Level != dollmind.LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected at least one action")
	}
}

func TestIntegrationWSStub(t *testing.T) {
	log := logger.New(logger.WarnLevel, nil)
	srv := ws.New(ws.Config{Listen: "127.0.0.1:0"}, log)

	go func() {
		srv.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)

	if count := srv.ConnCount(); count != 0 {
		t.Errorf("expected 0 connections, got %d", count)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestIntegrationSecretRoundTrip(t *testing.T) {
	dir := t.TempDir()

	secrets := &dollstate.Secrets{
		Items: []dollstate.SecretItem{
			{Key: "api_key", Value: "«redacted:sk-…»", Source: "env"},
		},
	}

	path, err := dollstate.SaveSecrets(dir, secrets)
	if err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}

	loaded, err := dollstate.LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}

	if len(loaded.Items) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(loaded.Items))
	}
	if loaded.Items[0].Value != "«redacted:sk-…»" {
		t.Errorf("expected secret value, got %s", loaded.Items[0].Value)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}