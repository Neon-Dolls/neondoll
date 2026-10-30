package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTelegramDriverRequiresPinnedSource(t *testing.T) {
	driver := NewTelegramDriver(TelegramDriverConfig{Revision: TelegramGatewayRevision})
	defer driver.Close()
	if _, err := driver.Artifact(context.Background()); err == nil {
		t.Fatal("missing real Telegram source was accepted")
	}
}

func TestTelegramDriverRejectsWrongRevisionBeforeArtifact(t *testing.T) {
	directory := t.TempDir()
	runGit := func(arguments ...string) {
		command := append([]string{"-C", directory}, arguments...)
		if _, err := commandOutput(context.Background(), "", "git", command...); err != nil {
			t.Fatal(err)
		}
	}
	runGit("init")
	runGit("config", "user.email", "acceptance@example.invalid")
	runGit("config", "user.name", "Acceptance")
	if err := os.WriteFile(filepath.Join(directory, "README"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README")
	runGit("commit", "-m", "fixture")
	driver := NewTelegramDriver(TelegramDriverConfig{SourceDir: directory, Artifact: filepath.Join(directory, "missing.wasm"), Revision: TelegramGatewayRevision})
	defer driver.Close()
	if _, err := driver.Artifact(context.Background()); err == nil {
		t.Fatal("unpinned checkout was accepted")
	}
}

func TestTelegramProfileDoesNotSkipUnavailableGateway(t *testing.T) {
	driver := NewTelegramDriver(TelegramDriverConfig{Revision: TelegramGatewayRevision})
	defer driver.Close()
	runner, err := NewRunner(Core5TelegramProfile(driver))
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), "core5", "test", map[string]string{"neondoll-gateway-telegram": driver.Revision()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Scenarios {
		if result.ID == ScenarioTelegramInbound || result.ID == ScenarioTelegramOutbound || result.ID == ScenarioTelegramPermission {
			if result.Status != StatusFail {
				t.Fatalf("%s status=%s, want fail", result.ID, result.Status)
			}
		}
	}
}

func TestTelegramInvalidArtifactFailsStartup(t *testing.T) {
	directory := t.TempDir()
	runGit := func(arguments ...string) {
		command := append([]string{"-C", directory}, arguments...)
		if _, err := commandOutput(context.Background(), "", "git", command...); err != nil {
			t.Fatal(err)
		}
	}
	runGit("init")
	runGit("config", "user.email", "acceptance@example.invalid")
	runGit("config", "user.name", "Acceptance")
	artifact := filepath.Join(directory, "gateway.wasm")
	if err := os.WriteFile(artifact, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "gateway.wasm")
	runGit("commit", "-m", "invalid fixture")
	revision, err := commandOutput(context.Background(), directory, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	driver := NewTelegramDriver(TelegramDriverConfig{SourceDir: directory, Artifact: artifact, Revision: stringTrim(revision)})
	defer driver.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = newTelegramFixture(ctx, driver, telegramFixtureOptions{handler: silentInteractionHandler{}, mappings: ownerMappings()})
	if err == nil {
		t.Fatal("invalid WASM artifact initialized")
	}
}

func stringTrim(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
