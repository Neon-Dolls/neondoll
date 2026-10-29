package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShellBodyDriverFailsWhenExternalParticipantUnavailable(t *testing.T) {
	driver := NewShellBodyDriver(ShellBodyDriverConfig{Binary: filepath.Join(t.TempDir(), "missing"), Revision: ShellBodyRevision})
	if err := driver.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Prepare error=%v", err)
	}
	runner, err := NewRunner(Core5ExternalProfile(driver))
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), "core5", "core", map[string]string{"neondoll-shellbody": ShellBodyRevision}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Successful() {
		t.Fatal("unavailable external participant reported success")
	}
	for _, result := range report.Scenarios[3:] {
		if result.Status != StatusFail {
			t.Fatalf("external result=%#v", result)
		}
	}
}

func TestShellBodyDriverRejectsUnpinnedCheckout(t *testing.T) {
	driver := NewShellBodyDriver(ShellBodyDriverConfig{SourceDir: t.TempDir(), Revision: ShellBodyRevision})
	if err := driver.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("Prepare error=%v", err)
	}
}

func TestManagedProcessStopKillsAndReapsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group lifecycle test")
	}
	process, err := startManagedProcess(context.Background(), "/bin/sh", "-c", "trap '' INT; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = process.Stop(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !process.Exited() {
		t.Fatal("child was not reaped")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("forced cleanup exceeded bound")
	}
}

func TestShellBodyCapabilityReadinessTimesOutAndCleansProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell fixture")
	}
	directory := t.TempDir()
	script := filepath.Join(directory, "fake-shellbody")
	contents := "#!/bin/sh\ncase \" $* \" in\n*' --enroll '*) echo 'enrolled body_id=fake-body'; exit 0;;\n*' --serve '*) trap 'exit 0' INT TERM; while :; do sleep 1; done;;\n*) exit 0;;\nesac\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	driver := NewShellBodyDriver(ShellBodyDriverConfig{Binary: script, Revision: ShellBodyRevision, Timeout: 150 * time.Millisecond})
	session, err := driver.connect(context.Background())
	if session != nil {
		session.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("connect error=%v", err)
	}
}
