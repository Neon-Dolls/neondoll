// Package openviking implements the Core Memory Provider contract using the
// documented OpenViking HTTP API. OpenViking owns the retained representation;
// Core does not keep a parallel memory journal.
package openviking

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

const (
	APIVersion                        = "v1"
	TargetOpenVikingRevision          = "9ac008f9b86c91b79d6e93eb8df8ea7f4d48cecb"
	DefaultTimeout                    = 30 * time.Second
	DefaultMaxInlineBytes      uint64 = 4 << 20
	DefaultMaxStreamBytes      uint64 = 16 << 20
	DefaultMaxReplayKeys              = 1024
	DefaultMaxRecallCandidates        = 4000
)

type Config struct {
	Endpoint            string
	APIKey              string
	UserID              string
	DollID              string
	MemoryType          string
	Namespace           string
	Timeout             time.Duration
	MaxInlineBytes      uint64
	MaxStreamBytes      uint64
	MaxReplayKeys       int
	MaxRecallCandidates uint32
	HTTPClient          *http.Client
}

type Provider struct {
	endpoint, apiKey, root string
	client                 *http.Client
	maxInline, maxStream   uint64
	maxReplayKeys          int
	maxRecallCandidates    uint32
	timeout                time.Duration
	mu                     sync.Mutex
	closeOnce              sync.Once
	closed                 atomic.Bool
}

var _ memory.Provider = (*Provider)(nil)

func Open(cfg Config) (*Provider, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if cfg.Endpoint == "" || strings.TrimSpace(cfg.UserID) == "" || strings.TrimSpace(cfg.DollID) == "" {
		return nil, fmt.Errorf("%w: endpoint, user ID, and doll ID are required", memory.ErrInvalidRequest)
	}
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid OpenViking endpoint", memory.ErrInvalidRequest)
	}
	if !safeSegment(cfg.UserID) || !safeSegment(cfg.DollID) || (cfg.MemoryType != "" && !safeSegment(cfg.MemoryType)) || (cfg.Namespace != "" && !safeSegment(cfg.Namespace)) {
		return nil, fmt.Errorf("%w: invalid namespace component", memory.ErrInvalidRequest)
	}
	applyDefaults(&cfg)
	if cfg.MaxReplayKeys < 0 {
		return nil, fmt.Errorf("%w: negative replay-key limit", memory.ErrInvalidRequest)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{
		endpoint: cfg.Endpoint, apiKey: cfg.APIKey, client: client,
		root:      fmt.Sprintf("viking://user/%s/memories/%s/%s/%s", cfg.UserID, cfg.MemoryType, cfg.Namespace, cfg.DollID),
		maxInline: cfg.MaxInlineBytes, maxStream: cfg.MaxStreamBytes,
		maxReplayKeys: cfg.MaxReplayKeys, maxRecallCandidates: cfg.MaxRecallCandidates,
		timeout: cfg.Timeout,
	}, nil
}

func applyDefaults(cfg *Config) {
	if cfg.MemoryType == "" {
		cfg.MemoryType = "experiences"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "neondoll"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxInlineBytes == 0 {
		cfg.MaxInlineBytes = DefaultMaxInlineBytes
	}
	if cfg.MaxStreamBytes == 0 {
		cfg.MaxStreamBytes = DefaultMaxStreamBytes
	}
	if cfg.MaxReplayKeys == 0 {
		cfg.MaxReplayKeys = DefaultMaxReplayKeys
	}
	if cfg.MaxRecallCandidates == 0 {
		cfg.MaxRecallCandidates = DefaultMaxRecallCandidates
	}
}

func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#") && strings.TrimSpace(s) == s
}

func (p *Provider) Capabilities(context.Context) (memory.Capabilities, error) {
	return memory.Capabilities{
		ContractVersion: memory.ContractVersion, ContentTypes: []string{"text/plain", "application/json"},
		MaxInlineBytes: p.maxInline, MaxStreamBytes: p.maxStream,
		SupportsUpdates: true, SupportsConsolidation: false,
		Consistency: memory.ConsistencyCapabilities{
			ReplayScope: memory.ReplayScopeRecord, ReplayDurable: true,
			RevisionConflicts:  memory.RevisionConflictsSingleWriter,
			ReadAfterWrite:     memory.ReadAfterWriteEventual,
			ImportAtomicity:    memory.ImportAtomicityPerRecord,
			RetrievalSemantics: memory.RetrievalProviderNative,
		},
	}, nil
}

func (p *Provider) Health(ctx context.Context) (memory.Health, error) {
	var result any
	if err := p.call(ctx, http.MethodGet, "/api/v1/system/status", nil, nil, &result, nil); err != nil {
		return memory.Health{Status: memory.HealthUnavailable, Detail: "OpenViking API unavailable"}, err
	}
	return memory.Health{Status: memory.HealthHealthy, Detail: "OpenViking API reachable"}, nil
}

func (p *Provider) Close() error {
	p.closeOnce.Do(func() { p.closed.Store(true); p.client.CloseIdleConnections() })
	return nil
}
