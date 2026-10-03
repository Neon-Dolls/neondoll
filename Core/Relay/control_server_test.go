package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// ── A2: Registration via ControlServer boundary ────────────────────────
//
// These tests exercise the actual ControlServer boundary (registration
// via the control server), not just the isolated VerifyRegistrationToken
// unit function.

// TestControlServer_ValidCredential proves that a configured valid credential
// registers successfully (registry registration count increases by 1).
func TestControlServer_ValidCredential(t *testing.T) {
	t.Parallel()

	// Configure one credential.
	hash := sha256.Sum256([]byte("valid-token"))
	credHash := hex.EncodeToString(hash[:])

	cfg := DefaultServiceConfig()
	cfg.Credentials = []string{credHash}
	svc, err := NewService(cfg, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	before := svc.Registry().RegistrationCount()

	cfg2 := DefaultClientConfig()
	cfg2.RelayURL = fmt.Sprintf("ws://%s/relay", addr)
	cfg2.HandshakeTimeout = 5 * time.Second
	cfg2.ReadTimeout = 5 * time.Second
	cfg2.PingInterval = 30 * time.Second
	cfg2.RegistrationToken = "valid-token"

	client := NewControlClient(cfg2)
	err = client.Start(context.Background())
	if err != nil {
		t.Fatalf("client.Start with valid token: %v", err)
	}
	defer client.Shutdown()

	after := svc.Registry().RegistrationCount()
	if after != before+1 {
		t.Fatalf("registrations before=%d after=%d; want %d (+1)", before, after, before+1)
	}
}

// TestControlServer_InvalidCredential proves that an invalid credential is
// rejected AND the registry registration count remains unchanged.
func TestControlServer_InvalidCredential(t *testing.T) {
	t.Parallel()

	hash := sha256.Sum256([]byte("real-token"))
	credHash := hex.EncodeToString(hash[:])

	cfg := DefaultServiceConfig()
	cfg.Credentials = []string{credHash}
	svc, err := NewService(cfg, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	before := svc.Registry().RegistrationCount()

	cfg2 := DefaultClientConfig()
	cfg2.RelayURL = fmt.Sprintf("ws://%s/relay", addr)
	cfg2.HandshakeTimeout = 2 * time.Second
	cfg2.ReadTimeout = 5 * time.Second
	cfg2.PingInterval = 30 * time.Second
	cfg2.RegistrationToken = "wrong-token"

	client := NewControlClient(cfg2)
	ccCtx, ccCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccCancel()
	err = client.Start(ccCtx)
	if err == nil {
		client.Shutdown()
		t.Fatal("client.Start with invalid token: err = nil; expected auth error")
	}

	after := svc.Registry().RegistrationCount()
	if after != before {
		t.Fatalf("registrations before=%d after=%d; want %d (unchanged)", before, after, before)
	}
}

// TestControlServer_EmptyToken proves that an empty credential is rejected
// AND the registry registration count remains unchanged.
func TestControlServer_EmptyToken(t *testing.T) {
	t.Parallel()

	// Configure one credential so the check is meaningful.
	hash := sha256.Sum256([]byte("any-token"))
	credHash := hex.EncodeToString(hash[:])

	cfg := DefaultServiceConfig()
	cfg.Credentials = []string{credHash}
	svc, err := NewService(cfg, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	before := svc.Registry().RegistrationCount()

	cfg2 := DefaultClientConfig()
	cfg2.RelayURL = fmt.Sprintf("ws://%s/relay", addr)
	cfg2.HandshakeTimeout = 2 * time.Second
	cfg2.ReadTimeout = 5 * time.Second
	cfg2.PingInterval = 30 * time.Second
	cfg2.RegistrationToken = ""

	client := NewControlClient(cfg2)
	ccCtx, ccCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccCancel()
	err = client.Start(ccCtx)
	if err == nil {
		client.Shutdown()
		t.Fatal("client.Start with empty token: err = nil; expected auth error")
	}

	after := svc.Registry().RegistrationCount()
	if after != before {
		t.Fatalf("registrations before=%d after=%d; want %d (unchanged)", before, after, before)
	}
}

// TestControlServer_NoVerifiers proves that no configured verifiers rejects
// registration AND creates no state (registration count unchanged).
func TestControlServer_NoVerifiers(t *testing.T) {
	t.Parallel()

	cfg := DefaultServiceConfig()
	cfg.Credentials = []string{} // No credential verifiers configured.
	svc, err := NewService(cfg, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	before := svc.Registry().RegistrationCount()

	cfg2 := DefaultClientConfig()
	cfg2.RelayURL = fmt.Sprintf("ws://%s/relay", addr)
	cfg2.HandshakeTimeout = 2 * time.Second
	cfg2.ReadTimeout = 5 * time.Second
	cfg2.PingInterval = 30 * time.Second
	cfg2.RegistrationToken = "any-token"

	client := NewControlClient(cfg2)
	ccCtx, ccCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccCancel()
	err = client.Start(ccCtx)
	if err == nil {
		client.Shutdown()
		t.Fatal("client.Start with no verifiers: err = nil; expected auth error")
	}

	after := svc.Registry().RegistrationCount()
	if after != before {
		t.Fatalf("registrations before=%d after=%d; want %d (unchanged)", before, after, before)
	}
}
