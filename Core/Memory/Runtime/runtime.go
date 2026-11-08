// Package memoryruntime is the replaceable composition boundary for the
// built-in Go Memory Provider adapters. Provider modules will replace this
// constructor without changing consumers of memory.Provider.
package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
	hindsight "github.com/Neon-Dolls/neondoll/Core/Memory/Hindsight"
	openviking "github.com/Neon-Dolls/neondoll/Core/Memory/OpenViking"
	sqlite "github.com/Neon-Dolls/neondoll/Core/Memory/SQLite"
)

var ErrNotConfigured = errors.New("memory runtime: active provider is not configured")

type closeProvider interface {
	memory.Provider
	Close() error
}

// Managed owns one initialized provider and makes shutdown idempotent.
type Managed struct {
	memory.Provider
	name   string
	closer closeProvider
	once   sync.Once
	err    error
}

func (m *Managed) Name() string { return m.name }
func (m *Managed) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() { m.err = m.closer.Close() })
	return m.err
}

type Options struct {
	SecretsRoot string
	DataRoot    string
	HTTPClient  *http.Client
}

// OpenActive constructs and health-checks exactly the explicitly selected
// provider. It never chooses a fallback. A failure closes partial resources.
func OpenActive(ctx context.Context, cfg memory.Configuration, dollID string, opts Options) (*Managed, error) {
	resolved, ok, err := cfg.ResolveActive()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(dollID) == "" {
		return nil, fmt.Errorf("%w: Doll ID is required", memory.ErrInvalidConfiguration)
	}
	ref := resolved.Reference
	if ref.Implementation == "" {
		return nil, fmt.Errorf("%w: %q is a module-managed reference without a built-in implementation", ErrNotConfigured, resolved.Name)
	}
	var provider closeProvider
	switch ref.Implementation {
	case memory.ImplementationSQLite:
		settings := ref.SQLite
		var path string
		path, err = resolveSQLitePath(opts.DataRoot, settings.Path)
		if err == nil {
			provider, err = sqlite.Open(sqlite.Config{Path: path, DollID: dollID, MaxInlineBytes: settings.MaxInlineBytes, MaxStreamBytes: settings.MaxStreamBytes})
		}
	case memory.ImplementationOpenViking:
		settings := ref.OpenViking
		var key string
		key, err = readSecret(opts.SecretsRoot, settings.APIKeySecret)
		if err == nil {
			provider, err = openviking.Open(openviking.Config{Endpoint: settings.Endpoint, APIKey: key, UserID: settings.UserID, DollID: dollID, MemoryType: settings.MemoryType, Namespace: settings.Namespace, Timeout: time.Duration(settings.TimeoutSeconds) * time.Second, MaxInlineBytes: settings.MaxInlineBytes, MaxStreamBytes: settings.MaxStreamBytes, MaxReplayKeys: settings.MaxReplayKeys, MaxRecallCandidates: settings.MaxRecallCandidates, HTTPClient: opts.HTTPClient})
		}
	case memory.ImplementationHindsight:
		settings := ref.Hindsight
		var token string
		token, err = readSecret(opts.SecretsRoot, settings.TokenSecret)
		if err == nil {
			provider, err = hindsight.Open(hindsight.Config{Endpoint: settings.Endpoint, Token: token, DollID: dollID, Namespace: settings.Namespace, Timeout: time.Duration(settings.TimeoutSeconds) * time.Second, MaxInlineBytes: settings.MaxInlineBytes, MaxStreamBytes: settings.MaxStreamBytes, MaxRecallTokens: settings.MaxRecallTokens, PageSize: settings.PageSize, MaxDocumentMemories: settings.MaxDocumentMemories, MaxReplayKeys: settings.MaxReplayKeys, MaxRecallDocuments: settings.MaxRecallDocuments, HTTPClient: opts.HTTPClient})
		}
	default:
		err = fmt.Errorf("%w: unknown implementation %q", memory.ErrInvalidConfiguration, ref.Implementation)
	}
	if err != nil {
		return nil, fmt.Errorf("memory runtime: open %q: %w", resolved.Name, err)
	}
	health, healthErr := provider.Health(ctx)
	if healthErr != nil || health.Status == memory.HealthUnavailable {
		_ = provider.Close()
		if healthErr != nil {
			return nil, fmt.Errorf("memory runtime: health %q: %w", resolved.Name, healthErr)
		}
		return nil, fmt.Errorf("memory runtime: health %q: %s", resolved.Name, health.Detail)
	}
	return &Managed{Provider: provider, closer: provider, name: resolved.Name}, nil
}

func resolveSQLitePath(dataRoot, configured string) (string, error) {
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured), nil
	}
	if strings.TrimSpace(dataRoot) == "" {
		return "", fmt.Errorf("%w: data root is required for a relative SQLite path", memory.ErrInvalidConfiguration)
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", fmt.Errorf("%w: resolve data root: %v", memory.ErrInvalidConfiguration, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve data root: %v", memory.ErrInvalidConfiguration, err)
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.Clean(configured)))
	if err != nil || !within(root, target) {
		return "", fmt.Errorf("%w: SQLite path escapes data root", memory.ErrInvalidConfiguration)
	}

	// The database may not exist yet. Resolve the nearest existing ancestor so
	// symlinked directories cannot move the eventual file outside dataRoot.
	ancestor := target
	for {
		_, statErr := os.Lstat(ancestor)
		if statErr == nil {
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("%w: inspect SQLite path: %v", memory.ErrInvalidConfiguration, statErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("%w: SQLite path has no existing parent", memory.ErrInvalidConfiguration)
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil || !within(root, resolvedAncestor) {
		return "", fmt.Errorf("%w: SQLite path symlink escapes data root", memory.ErrInvalidConfiguration)
	}
	remainder, err := filepath.Rel(ancestor, target)
	if err != nil {
		return "", fmt.Errorf("%w: resolve SQLite path: %v", memory.ErrInvalidConfiguration, err)
	}
	return filepath.Join(resolvedAncestor, remainder), nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// An empty secret reference means that the backend is configured without
// authentication. A non-empty reference must resolve inside SecretsRoot.
func readSecret(root, reference string) (string, error) {
	if reference == "" {
		return "", nil
	}
	if root == "" || filepath.IsAbs(reference) {
		return "", errors.New("memory runtime: invalid secret reference")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("memory runtime: secrets root: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(rootAbs, filepath.Clean(reference)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("memory runtime: secret escapes secrets root")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("memory runtime: read secret %q: %w", reference, err)
	}
	rel, err = filepath.Rel(rootAbs, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("memory runtime: secret symlink escapes secrets root")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("memory runtime: read secret %q: %w", reference, err)
	}
	return strings.TrimSpace(string(data)), nil
}
