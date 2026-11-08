package main

import (
	"errors"
	"testing"
)

func runLaterStartupStep(cleanup *daemonCleanup, step func() error) (err error) {
	defer cleanup.Close()
	return step()
}

func TestDaemonCleanupClosesProviderAfterLaterStartupFailure(t *testing.T) {
	want := errors.New("later component failed")
	closed := 0
	cleanup := newDaemonCleanup(func(err error) { t.Fatalf("unexpected close error: %v", err) })
	cleanup.Add(func() error { closed++; return nil })

	err := runLaterStartupStep(cleanup, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("startup error = %v, want %v", err, want)
	}
	cleanup.Close()
	if closed != 1 {
		t.Fatalf("provider close count = %d, want 1", closed)
	}
}

func TestDaemonCleanupClosesProviderOnNormalShutdown(t *testing.T) {
	closed := 0
	cleanup := newDaemonCleanup(nil)
	cleanup.Add(func() error { closed++; return nil })
	if err := runLaterStartupStep(cleanup, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("provider close count = %d, want 1", closed)
	}
}

func TestDaemonCleanupWithoutMemoryProvider(t *testing.T) {
	cleanup := newDaemonCleanup(nil)
	if err := runLaterStartupStep(cleanup, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanup.Close()
}
