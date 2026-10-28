package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
)

type GatewayHost interface {
	Register(context.Context, gateway.Adapter) error
	Unregister(context.Context, externalidentity.GatewayInstanceID) error
}

type Status struct {
	InstanceID       string
	Enabled, Running bool
	ManagedBy, Error string
}
type installation struct {
	opMu     sync.Mutex
	mu       sync.Mutex
	manifest Manifest
	adapter  *GatewayAdapter
	running  bool
	failure  error
}
type Manager struct {
	mu                     sync.Mutex
	activationMu           sync.Mutex
	runtime                *WASMRuntime
	host                   GatewayHost
	dataRoot, installsRoot string
	entries                map[string]*installation
	closing                bool
	shutdownDone           chan struct{}
	shutdownErr            error
}

func NewManager(runtime *WASMRuntime, host GatewayHost, dataRoot string, configured []Manifest) (*Manager, error) {
	if runtime == nil || host == nil {
		return nil, ErrInvalidManifest
	}
	m := &Manager{runtime: runtime, host: host, dataRoot: dataRoot, installsRoot: filepath.Join(dataRoot, "extensions", "installed"), entries: map[string]*installation{}}
	for _, manifest := range configured {
		manifest.ManagedBy = ManagedConfiguration
		if err := m.add(manifest); err != nil {
			return nil, err
		}
	}
	if err := m.loadInstalled(); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) add(manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if m.entries[manifest.InstanceID] != nil {
		return ErrInvalidManifest
	}
	m.entries[manifest.InstanceID] = &installation{manifest: manifest}
	return nil
}
func (m *Manager) loadInstalled() error {
	entries, err := os.ReadDir(m.installsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.installsRoot, entry.Name(), "manifest.json"))
		if err != nil {
			return err
		}
		var manifest Manifest
		if json.Unmarshal(data, &manifest) != nil {
			return ErrInvalidManifest
		}
		manifest.ManagedBy = ManagedInstalled
		if err = m.add(manifest); err != nil {
			return err
		}
	}
	return nil
}

// Install validates integrity at the caller-selected local source, then copies
// the artifact and manifest into Core-controlled storage. Installed manifests
// reconstruct independently of the daemon configuration file.
func (m *Manager) Install(manifest Manifest) error {
	manifest.ManagedBy = ManagedInstalled
	if err := manifest.Validate(); err != nil {
		return err
	}
	source := manifest.Artifact.Path
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if !verifySHA256(data, manifest.Artifact.SHA256) {
		return ErrInvalidManifest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.entries[manifest.InstanceID] != nil {
		return ErrInvalidManifest
	}
	dir := filepath.Join(m.installsRoot, manifest.InstanceID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	artifact := filepath.Join(dir, "module.wasm")
	tmp, err := os.CreateTemp(dir, "module-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, artifact); err != nil {
		return err
	}
	rel, err := filepath.Rel(m.dataRoot, artifact)
	if err != nil {
		return err
	}
	manifest.Artifact.Path = rel
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestTemp, err := os.CreateTemp(dir, "manifest-*.tmp")
	if err != nil {
		return err
	}
	manifestName := manifestTemp.Name()
	defer os.Remove(manifestName)
	if err = manifestTemp.Chmod(0600); err != nil {
		manifestTemp.Close()
		return err
	}
	if _, err = manifestTemp.Write(encoded); err != nil {
		manifestTemp.Close()
		return err
	}
	if err = manifestTemp.Close(); err != nil {
		return err
	}
	if err = os.Rename(manifestName, filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}
	m.entries[manifest.InstanceID] = &installation{manifest: manifest}
	return nil
}

func (m *Manager) entry(id string) (*installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrInvalidManifest
	}
	entry := m.entries[id]
	if entry == nil {
		return nil, ErrInvalidManifest
	}
	return entry, nil
}
func (m *Manager) LoadEnabled(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.entries))
	for id, e := range m.entries {
		if e.manifest.Enabled {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := m.Enable(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) Enable(ctx context.Context, id string) error {
	entry, err := m.entry(id)
	if err != nil {
		return err
	}
	entry.opMu.Lock()
	defer entry.opMu.Unlock()
	m.mu.Lock()
	closing := m.closing
	m.mu.Unlock()
	if closing {
		return ErrInvalidManifest
	}
	entry.mu.Lock()
	if entry.running {
		entry.mu.Unlock()
		return nil
	}
	manifest := entry.manifest
	entry.mu.Unlock()
	if err = m.runtime.Load(ctx, manifest, m.dataRoot); err != nil {
		entry.mu.Lock()
		entry.failure = err
		entry.mu.Unlock()
		return err
	}
	adapter := NewGatewayAdapter(manifest, m.runtime)
	// Activation is the linearization boundary shared with Shutdown. Once
	// Shutdown marks the manager closing, no later Host registration is
	// possible, including an Enable that was waiting on this installation.
	m.activationMu.Lock()
	m.mu.Lock()
	closing = m.closing
	m.mu.Unlock()
	if closing {
		m.activationMu.Unlock()
		_ = m.runtime.Unload(ctx, id)
		return ErrInvalidManifest
	}
	if err = m.host.Register(ctx, adapter); err != nil {
		m.activationMu.Unlock()
		_ = m.runtime.Unload(ctx, id)
		entry.mu.Lock()
		entry.failure = err
		entry.mu.Unlock()
		return err
	}
	m.activationMu.Unlock()
	entry.mu.Lock()
	entry.adapter, entry.running, entry.failure = adapter, true, nil
	entry.mu.Unlock()
	return nil
}
func (m *Manager) Disable(ctx context.Context, id string) error {
	m.mu.Lock()
	entry := m.entries[id]
	m.mu.Unlock()
	if entry == nil {
		return nil
	}
	entry.opMu.Lock()
	defer entry.opMu.Unlock()
	entry.mu.Lock()
	if !entry.running {
		entry.mu.Unlock()
		return nil
	}
	entry.mu.Unlock()
	err := m.host.Unregister(ctx, externalidentity.GatewayInstanceID(id))
	entry.mu.Lock()
	defer entry.mu.Unlock()
	// Unregister returns only after the Host has made the adapter inactive and
	// drained it, even when Adapter.Stop reports an error.
	entry.adapter = nil
	entry.running = false
	if err != nil {
		entry.failure = err
		return err
	}
	entry.failure = nil
	return nil
}
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	entries := make(map[string]*installation, len(m.entries))
	for id, e := range m.entries {
		entries[id] = e
	}
	m.mu.Unlock()
	out := make([]Status, 0, len(entries))
	for id, e := range entries {
		e.mu.Lock()
		s := Status{InstanceID: id, Enabled: e.manifest.Enabled, Running: e.running, ManagedBy: e.manifest.ManagedBy}
		if e.failure != nil {
			s.Error = e.failure.Error()
		}
		e.mu.Unlock()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.activationMu.Lock()
	m.mu.Lock()
	if m.closing {
		done := m.shutdownDone
		m.mu.Unlock()
		m.activationMu.Unlock()
		select {
		case <-done:
			m.mu.Lock()
			err := m.shutdownErr
			m.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.closing = true
	m.shutdownDone = make(chan struct{})
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	m.activationMu.Unlock()
	sort.Strings(ids)
	var first error
	for _, id := range ids {
		m.mu.Lock()
		entry := m.entries[id]
		m.mu.Unlock()
		entry.opMu.Lock()
		entry.mu.Lock()
		running := entry.running
		entry.mu.Unlock()
		if running {
			err := m.host.Unregister(ctx, externalidentity.GatewayInstanceID(id))
			entry.mu.Lock()
			if err != nil {
				entry.failure = err
				if first == nil {
					first = err
				}
			}
			// Host.Unregister has completed its drain before returning.
			entry.running = false
			entry.adapter = nil
			entry.mu.Unlock()
		}
		entry.opMu.Unlock()
	}
	if err := m.runtime.Close(ctx); err != nil && first == nil {
		first = err
	}
	m.mu.Lock()
	m.shutdownErr = first
	close(m.shutdownDone)
	m.mu.Unlock()
	return first
}
