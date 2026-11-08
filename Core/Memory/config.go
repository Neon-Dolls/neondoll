package memory

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidConfiguration = errors.New("memory: invalid configuration")

type Configuration struct {
	Active    string                       `json:"active,omitempty"`
	Providers map[string]ProviderReference `json:"providers,omitempty"`
}

type ProviderReference struct {
	InstanceID      string                   `json:"instance_id"`
	ContractVersion int                      `json:"contract_version"`
	Implementation  string                   `json:"implementation,omitempty"`
	SQLite          *SQLiteConfiguration     `json:"sqlite,omitempty"`
	OpenViking      *OpenVikingConfiguration `json:"openviking,omitempty"`
	Hindsight       *HindsightConfiguration  `json:"hindsight,omitempty"`
}

const (
	ImplementationSQLite     = "sqlite"
	ImplementationOpenViking = "openviking"
	ImplementationHindsight  = "hindsight"
)

type SQLiteConfiguration struct {
	Path           string `json:"path"`
	MaxInlineBytes uint64 `json:"max_inline_bytes,omitempty"`
	MaxStreamBytes uint64 `json:"max_stream_bytes,omitempty"`
}

type OpenVikingConfiguration struct {
	Endpoint            string `json:"endpoint"`
	APIKeySecret        string `json:"api_key_secret,omitempty"`
	UserID              string `json:"user_id"`
	MemoryType          string `json:"memory_type,omitempty"`
	Namespace           string `json:"namespace,omitempty"`
	TimeoutSeconds      int64  `json:"timeout_seconds,omitempty"`
	MaxInlineBytes      uint64 `json:"max_inline_bytes,omitempty"`
	MaxStreamBytes      uint64 `json:"max_stream_bytes,omitempty"`
	MaxReplayKeys       int    `json:"max_replay_keys,omitempty"`
	MaxRecallCandidates uint32 `json:"max_recall_candidates,omitempty"`
}

type HindsightConfiguration struct {
	Endpoint            string `json:"endpoint"`
	TokenSecret         string `json:"token_secret,omitempty"`
	Namespace           string `json:"bank_namespace,omitempty"`
	TimeoutSeconds      int64  `json:"timeout_seconds,omitempty"`
	MaxInlineBytes      uint64 `json:"max_inline_bytes,omitempty"`
	MaxStreamBytes      uint64 `json:"max_stream_bytes,omitempty"`
	MaxRecallTokens     int    `json:"max_recall_tokens,omitempty"`
	PageSize            int    `json:"page_size,omitempty"`
	MaxDocumentMemories int    `json:"max_document_memories,omitempty"`
	MaxReplayKeys       int    `json:"max_replay_keys,omitempty"`
	MaxRecallDocuments  int    `json:"max_recall_documents,omitempty"`
}

type ResolvedProvider struct {
	Name            string
	InstanceID      string
	ContractVersion int
	Reference       ProviderReference
}

func (c Configuration) Validate() error {
	if len(c.Providers) == 0 {
		if c.Active != "" {
			return fmt.Errorf("%w: active provider is unknown", ErrInvalidConfiguration)
		}
		return nil
	}
	if _, ok := c.Providers[c.Active]; !ok {
		return fmt.Errorf("%w: active provider is unknown", ErrInvalidConfiguration)
	}
	instances := map[string]bool{}
	for name, ref := range c.Providers {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(ref.InstanceID) == "" || ref.ContractVersion != ContractVersion || instances[ref.InstanceID] {
			return ErrInvalidConfiguration
		}
		instances[ref.InstanceID] = true
		if err := ref.validateImplementation(); err != nil {
			return fmt.Errorf("%w: provider %q: %v", ErrInvalidConfiguration, name, err)
		}
	}
	return nil
}

func (c Configuration) ResolveActive() (ResolvedProvider, bool, error) {
	if err := c.Validate(); err != nil {
		return ResolvedProvider{}, false, err
	}
	if c.Active == "" {
		return ResolvedProvider{}, false, nil
	}
	ref := c.Providers[c.Active]
	return ResolvedProvider{Name: c.Active, InstanceID: ref.InstanceID, ContractVersion: ref.ContractVersion, Reference: ref}, true, nil
}

func (r ProviderReference) validateImplementation() error {
	configured := 0
	if r.SQLite != nil {
		configured++
	}
	if r.OpenViking != nil {
		configured++
	}
	if r.Hindsight != nil {
		configured++
	}
	if r.Implementation == "" {
		if configured != 0 {
			return errors.New("backend settings require an implementation")
		}
		return nil // module-managed v1 reference retained for compatibility
	}
	if configured != 1 {
		return errors.New("exactly one backend configuration is required")
	}
	switch r.Implementation {
	case ImplementationSQLite:
		if r.SQLite == nil || strings.TrimSpace(r.SQLite.Path) == "" {
			return errors.New("sqlite path is required")
		}
	case ImplementationOpenViking:
		if r.OpenViking == nil || strings.TrimSpace(r.OpenViking.Endpoint) == "" || strings.TrimSpace(r.OpenViking.UserID) == "" {
			return errors.New("openviking endpoint and user_id are required")
		}
		if r.OpenViking.TimeoutSeconds < 0 || r.OpenViking.MaxReplayKeys < 0 {
			return errors.New("openviking limits cannot be negative")
		}
	case ImplementationHindsight:
		if r.Hindsight == nil || strings.TrimSpace(r.Hindsight.Endpoint) == "" {
			return errors.New("hindsight endpoint is required")
		}
		if r.Hindsight.TimeoutSeconds < 0 || r.Hindsight.MaxRecallTokens < 0 || r.Hindsight.PageSize < 0 ||
			r.Hindsight.MaxDocumentMemories < 0 || r.Hindsight.MaxReplayKeys < 0 || r.Hindsight.MaxRecallDocuments < 0 {
			return errors.New("hindsight limits cannot be negative")
		}
	default:
		return fmt.Errorf("unknown implementation %q", r.Implementation)
	}
	return nil
}
