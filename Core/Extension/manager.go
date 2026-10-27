package extension

import (
	"context"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
)

type GatewayHost interface {
	Register(context.Context, gateway.Adapter) error
	Unregister(context.Context, externalidentity.GatewayInstanceID) error
}
type Status struct {
	InstanceID string
	Enabled    bool
	Running    bool
	Error      string
}
type Manager struct {
	mu        sync.Mutex
	runtime   *WASMRuntime
	host      GatewayHost
	dataRoot  string
	manifests map[string]Manifest
	running   map[string]*GatewayAdapter
	failures  map[string]error
}

func NewManager(runtime *WASMRuntime, host GatewayHost, dataRoot string, manifests []Manifest) (*Manager, error) {
	if runtime == nil || host == nil {
		return nil, ErrInvalidManifest
	}
	if err := ValidateManifests(manifests); err != nil {
		return nil, err
	}
	m := &Manager{runtime: runtime, host: host, dataRoot: dataRoot, manifests: map[string]Manifest{}, running: map[string]*GatewayAdapter{}, failures: map[string]error{}}
	for _, v := range manifests {
		m.manifests[v.InstanceID] = v
	}
	return m, nil
}

// Install validates and stages a local/offline manifest. Artifact integrity is
// rechecked when the installation is enabled.
func (m *Manager) Install(manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.manifests[manifest.InstanceID]; exists {
		return ErrInvalidManifest
	}
	m.manifests[manifest.InstanceID] = manifest
	return nil
}
func (m *Manager) LoadEnabled(ctx context.Context) error {
	for _, manifest := range m.manifests {
		if manifest.Enabled {
			if err := m.Enable(ctx, manifest.InstanceID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *Manager) Enable(ctx context.Context, id string) error {
	m.mu.Lock()
	manifest, ok := m.manifests[id]
	if !ok || m.running[id] != nil {
		m.mu.Unlock()
		return ErrInvalidManifest
	}
	m.mu.Unlock()
	if err := m.runtime.Load(ctx, manifest, m.dataRoot); err != nil {
		m.record(id, err)
		return err
	}
	adapter := NewGatewayAdapter(manifest, m.runtime)
	if err := m.host.Register(ctx, adapter); err != nil {
		_ = m.runtime.Unload(ctx, id)
		m.record(id, err)
		return err
	}
	m.mu.Lock()
	m.running[id] = adapter
	delete(m.failures, id)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Disable(ctx context.Context, id string) error {
	m.mu.Lock()
	adapter := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if adapter == nil {
		return nil
	}
	return m.host.Unregister(ctx, externalidentity.GatewayInstanceID(id))
}
func (m *Manager) record(id string, err error) { m.mu.Lock(); m.failures[id] = err; m.mu.Unlock() }
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.manifests))
	for id, v := range m.manifests {
		s := Status{InstanceID: id, Enabled: v.Enabled, Running: m.running[id] != nil}
		if err := m.failures[id]; err != nil {
			s.Error = err.Error()
		}
		out = append(out, s)
	}
	return out
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var first error
	for _, id := range ids {
		if err := m.Disable(ctx, id); err != nil && first == nil {
			first = err
		}
	}
	if err := m.runtime.Close(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
