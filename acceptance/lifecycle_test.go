package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
)

func TestLifecycleEvidenceIsMachineReadable(t *testing.T) {
	evidence := &LifecycleEvidence{RestartType: "forced", ProcessBoundary: "test process", RecoveredState: []string{"identity"}, ObservableOutcome: "continued"}
	runner, err := NewRunner(Profile{Name: "lifecycle", Scenarios: []Scenario{{ID: "evidence", Description: "evidence", Lifecycle: evidence, Run: func(context.Context) error { return nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), "lifecycle", "revision", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Scenarios[0].Lifecycle
	if got == nil || got.RestartType != "forced" || got.ProcessBoundary != "test process" || len(got.RecoveredState) != 1 || got.ObservableOutcome != "continued" {
		t.Fatalf("lifecycle evidence=%#v", got)
	}
}

func TestCorruptPersistentStoreFailsClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "core.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := persistence.NewStore(path); err == nil {
		store.Close()
		t.Fatal("corrupt persistent state opened successfully")
	}
}

func TestLifecycleProcessForcedCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group assertion")
	}
	command := exec.Command("sh", "-c", "trap '' TERM INT; while :; do sleep 1; done")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &lifecycleProcess{cmd: command, done: make(chan error, 1), output: &boundedBuffer{limit: 1024}}
	go func() { process.done <- command.Wait(); process.mu.Lock(); process.exited = true; process.mu.Unlock() }()
	instance := &daemonInstance{process: process}
	started := time.Now()
	if err := instance.stop(true); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("forced cleanup exceeded bound")
	}
}
