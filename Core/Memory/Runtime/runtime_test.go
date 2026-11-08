package memoryruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func configured(ref memory.ProviderReference) memory.Configuration {
	return memory.Configuration{Active: "primary", Providers: map[string]memory.ProviderReference{"primary": ref}}
}

func TestOpenSQLiteLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	m, err := OpenActive(context.Background(), configured(memory.ProviderReference{InstanceID: "local", ContractVersion: 1, Implementation: memory.ImplementationSQLite, SQLite: &memory.SQLiteConfiguration{Path: path}}), "doll-a", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Name() != "primary" {
		t.Fatalf("managed = %#v", m)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestResolveSQLitePathContainment(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, dataRoot, configured string
		want                       string
		wantErr                    bool
	}{
		{name: "relative", dataRoot: root, configured: "memory.db", want: filepath.Join(root, "memory.db")},
		{name: "nested new database", dataRoot: root, configured: "nested/memory.db", want: filepath.Join(nested, "memory.db")},
		{name: "traversal", dataRoot: root, configured: "../escaped.db", wantErr: true},
		{name: "absolute", dataRoot: root, configured: filepath.Join(outside, "absolute.db"), want: filepath.Join(outside, "absolute.db")},
		{name: "outside symlink", dataRoot: root, configured: "outside-link/escaped.db", wantErr: true},
		{name: "inside symlink", dataRoot: root, configured: "inside-link/safe.db", want: filepath.Join(inside, "safe.db")},
		{name: "missing root", dataRoot: filepath.Join(root, "missing"), configured: "memory.db", wantErr: true},
		{name: "empty root", configured: "memory.db", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSQLitePath(test.dataRoot, test.configured)
			if (err != nil) != test.wantErr {
				t.Fatalf("resolveSQLitePath() error = %v", err)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("resolveSQLitePath() = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected path created outside database: %v", err)
	}
}

func TestOpenSQLiteRelativePathCreatesOnlyInsideDataRoot(t *testing.T) {
	root := t.TempDir()
	ref := memory.ProviderReference{InstanceID: "local", ContractVersion: 1, Implementation: memory.ImplementationSQLite, SQLite: &memory.SQLiteConfiguration{Path: "nested/memory.db"}}
	m, err := OpenActive(context.Background(), configured(ref), "doll-a", Options{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "memory.db")); err != nil {
		t.Fatalf("database not created inside root: %v", err)
	}
}

func TestOpenHTTPProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"healthy"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"bank"}`))
	}))
	defer server.Close()
	secrets := t.TempDir()
	if err := os.WriteFile(filepath.Join(secrets, "token"), []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []memory.ProviderReference{
		{InstanceID: "ov", ContractVersion: 1, Implementation: memory.ImplementationOpenViking, OpenViking: &memory.OpenVikingConfiguration{Endpoint: server.URL, UserID: "user", APIKeySecret: "token"}},
		{InstanceID: "hs", ContractVersion: 1, Implementation: memory.ImplementationHindsight, Hindsight: &memory.HindsightConfiguration{Endpoint: server.URL, TokenSecret: "token"}},
	}
	for _, ref := range tests {
		m, err := OpenActive(context.Background(), configured(ref), "doll-a", Options{SecretsRoot: secrets, HTTPClient: server.Client()})
		if err != nil {
			t.Fatalf("%s: %v", ref.Implementation, err)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenFailsWithoutFallback(t *testing.T) {
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer unavailable.Close()
	ref := memory.ProviderReference{InstanceID: "remote", ContractVersion: 1, Implementation: memory.ImplementationOpenViking, OpenViking: &memory.OpenVikingConfiguration{Endpoint: unavailable.URL, UserID: "user"}}
	if m, err := OpenActive(context.Background(), configured(ref), "doll", Options{HTTPClient: unavailable.Client()}); err == nil || m != nil {
		t.Fatalf("OpenActive() = %#v, %v", m, err)
	}
	if _, err := os.Stat("neondoll-memory.sqlite"); !os.IsNotExist(err) {
		t.Fatalf("unexpected fallback database: %v", err)
	}
}

func TestOpenRejectsMissingAndEscapingSecrets(t *testing.T) {
	root := t.TempDir()
	ref := memory.ProviderReference{InstanceID: "remote", ContractVersion: 1, Implementation: memory.ImplementationOpenViking, OpenViking: &memory.OpenVikingConfiguration{Endpoint: "http://localhost", UserID: "user", APIKeySecret: "missing"}}
	if _, err := OpenActive(context.Background(), configured(ref), "doll", Options{SecretsRoot: root}); err == nil || !strings.Contains(err.Error(), "read secret") {
		t.Fatalf("missing secret: %v", err)
	}
	ref.OpenViking.APIKeySecret = "../outside"
	if _, err := OpenActive(context.Background(), configured(ref), "doll", Options{SecretsRoot: root}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping secret: %v", err)
	}
}

func TestLegacyReferenceFailsExplicitly(t *testing.T) {
	ref := memory.ProviderReference{InstanceID: "module-instance", ContractVersion: 1}
	if _, err := OpenActive(context.Background(), configured(ref), "doll", Options{}); err == nil || !strings.Contains(err.Error(), "module-managed") {
		t.Fatalf("OpenActive() = %v", err)
	}
}

func TestNoMemoryConfigurationReturnsNil(t *testing.T) {
	m, err := OpenActive(context.Background(), memory.Configuration{}, "doll", Options{})
	if err != nil || m != nil {
		t.Fatalf("OpenActive() = %#v, %v", m, err)
	}
}
