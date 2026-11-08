// Package hindsight implements Core's Memory Provider contract using
// Hindsight's documented HTTP API. Hindsight remains the sole owner of the
// retained documents and extracted memory units.
package hindsight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	TargetHindsightVersion            = "1.2.0"
	TargetHindsightRevision           = "0bd55015393d224deec1073d63b24bcc0faad164"
	DefaultTimeout                    = 30 * time.Second
	DefaultMaxInlineBytes      uint64 = 4 << 20
	DefaultMaxStreamBytes      uint64 = 16 << 20
	DefaultRecallTokens               = 4096
	DefaultPageSize                   = 100
	DefaultMaxDocumentMemories        = 1000
)

type Config struct {
	Endpoint, Token, DollID, Namespace string
	Timeout                            time.Duration
	MaxInlineBytes, MaxStreamBytes     uint64
	MaxRecallTokens, PageSize          int
	MaxDocumentMemories                int
	HTTPClient                         *http.Client
}

type Provider struct {
	endpoint, token, bankID   string
	client                    *http.Client
	timeout                   time.Duration
	maxInline, maxStream      uint64
	maxRecallTokens, pageSize int
	maxDocumentMemories       int
	mu                        sync.Mutex
	closeOnce                 sync.Once
	closed                    atomic.Bool
}

var _ memory.Provider = (*Provider)(nil)

func Open(cfg Config) (*Provider, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if cfg.Endpoint == "" || strings.TrimSpace(cfg.DollID) == "" {
		return nil, fmt.Errorf("%w: endpoint and Doll ID are required", memory.ErrInvalidRequest)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid Hindsight endpoint", memory.ErrInvalidRequest)
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "neondoll"
	}
	if !safeName(cfg.Namespace) {
		return nil, fmt.Errorf("%w: invalid namespace", memory.ErrInvalidRequest)
	}
	applyDefaults(&cfg)
	if cfg.MaxRecallTokens <= 0 || cfg.PageSize <= 0 || cfg.MaxDocumentMemories <= 0 {
		return nil, fmt.Errorf("%w: provider resource limits must be positive", memory.ErrInvalidRequest)
	}
	sum := sha256.Sum256([]byte(cfg.DollID))
	bankID := cfg.Namespace + "-" + hex.EncodeToString(sum[:16])
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{endpoint: cfg.Endpoint, token: cfg.Token, bankID: bankID,
		client: client, timeout: cfg.Timeout, maxInline: cfg.MaxInlineBytes,
		maxStream: cfg.MaxStreamBytes, maxRecallTokens: cfg.MaxRecallTokens,
		pageSize: cfg.PageSize, maxDocumentMemories: cfg.MaxDocumentMemories}, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxInlineBytes == 0 {
		cfg.MaxInlineBytes = DefaultMaxInlineBytes
	}
	if cfg.MaxStreamBytes == 0 {
		cfg.MaxStreamBytes = DefaultMaxStreamBytes
	}
	if cfg.MaxRecallTokens == 0 {
		cfg.MaxRecallTokens = DefaultRecallTokens
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.MaxDocumentMemories == 0 {
		cfg.MaxDocumentMemories = DefaultMaxDocumentMemories
	}
}

func safeName(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\?#")
}

func (p *Provider) Capabilities(context.Context) (memory.Capabilities, error) {
	return memory.Capabilities{ContractVersion: memory.ContractVersion,
		ContentTypes:   []string{"text/plain", "application/json"},
		MaxInlineBytes: p.maxInline, MaxStreamBytes: p.maxStream,
		SupportsUpdates: true, SupportsConsolidation: true,
		Consistency: memory.ConsistencyCapabilities{
			ReplayScope: memory.ReplayScopeRecord, ReplayDurable: true,
			RevisionConflicts:  memory.RevisionConflictsSingleWriter,
			ReadAfterWrite:     memory.ReadAfterWriteEventual,
			ImportAtomicity:    memory.ImportAtomicityPerRecord,
			RetrievalSemantics: memory.RetrievalProviderNative,
		}}, nil
}

func (p *Provider) Health(ctx context.Context) (memory.Health, error) {
	var bank map[string]any
	if err := p.call(ctx, http.MethodGet, p.bankPath(), nil, nil, &bank); err != nil {
		return memory.Health{Status: memory.HealthUnavailable, Detail: "Hindsight bank unavailable"}, err
	}
	return memory.Health{Status: memory.HealthHealthy, Detail: "Hindsight bank reachable"}, nil
}

func (p *Provider) Close() error {
	p.closeOnce.Do(func() { p.closed.Store(true); p.client.CloseIdleConnections() })
	return nil
}

func (p *Provider) bankPath() string { return "/v1/default/banks/" + url.PathEscape(p.bankID) }
