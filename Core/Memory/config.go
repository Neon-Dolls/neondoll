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
	InstanceID      string `json:"instance_id"`
	ContractVersion int    `json:"contract_version"`
}

type ResolvedProvider struct {
	Name            string
	InstanceID      string
	ContractVersion int
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
	return ResolvedProvider{Name: c.Active, InstanceID: ref.InstanceID, ContractVersion: ref.ContractVersion}, true, nil
}
