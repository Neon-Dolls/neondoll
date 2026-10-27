package gateway

import (
	"context"
	"strings"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

type IngressFactory func(externalidentity.GatewayInstanceID) (*interaction.GatewayIngress, error)
type ReplayFunc func(context.Context) error
type HostOption func(*Host)

func WithSessionService(sessions session.Service) HostOption {
	return func(host *Host) { host.sessions = sessions }
}

type managedAdapter struct {
	adapter    Adapter
	version    int
	active     bool
	startDone  chan struct{}
	deliveries sync.WaitGroup
	stopOnce   sync.Once
	stopErr    error
}

func newManagedAdapter(adapter Adapter, version int) *managedAdapter {
	return &managedAdapter{adapter: adapter, version: version, startDone: make(chan struct{})}
}

func (m *managedAdapter) stop(ctx context.Context) error {
	m.deliveries.Wait()
	m.stopOnce.Do(func() { m.stopErr = m.adapter.Stop(ctx) })
	return m.stopErr
}

// Host owns installed Gateway lifecycles and is the production SessionEvent
// transport. Host.mu is never held across adapter code or waits. Delivery Add
// and lifecycle exclusion are serialized under Host.mu before Wait begins.
type Host struct {
	mu       sync.Mutex
	adapters map[externalidentity.GatewayInstanceID]*managedAdapter
	ingress  IngressFactory
	replay   ReplayFunc
	sessions session.Service
	closed   bool
}

func NewHost() *Host {
	return &Host{adapters: make(map[externalidentity.GatewayInstanceID]*managedAdapter)}
}

func (h *Host) Configure(ingress IngressFactory, replay ReplayFunc, options ...HostOption) error {
	if h == nil || ingress == nil || replay == nil {
		return ErrHostNotConfigured
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.adapters) != 0 {
		return ErrHostNotConfigured
	}
	h.ingress, h.replay = ingress, replay
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return nil
}

func (h *Host) Register(ctx context.Context, adapter Adapter) error {
	if h == nil || adapter == nil {
		return ErrInvalidAdapter
	}
	id := adapter.InstanceID()
	version := adapter.SessionEventVersion()
	if strings.TrimSpace(string(id)) == "" || (version != LegacySessionEventVersion && version != StructuredSessionEventVersion) {
		return ErrInvalidAdapter
	}
	managed := newManagedAdapter(adapter, version)
	h.mu.Lock()
	if h.closed || h.ingress == nil || h.replay == nil {
		h.mu.Unlock()
		return ErrHostNotConfigured
	}
	if _, exists := h.adapters[id]; exists {
		h.mu.Unlock()
		return ErrAlreadyRegistered
	}
	// Reserve the instance before invoking factory or adapter code.
	h.adapters[id] = managed
	factory, replay := h.ingress, h.replay
	h.mu.Unlock()

	ingress, err := factory(id)
	if err == nil {
		err = adapter.Start(ctx, &boundIngress{version: version, gateway: id, sessions: h.sessions, ingress: ingress, replay: replay})
	}

	h.mu.Lock()
	close(managed.startDone)
	current := h.adapters[id] == managed
	if err != nil || h.closed || !current {
		if current {
			delete(h.adapters, id)
		}
		h.mu.Unlock()
		_ = managed.stop(context.Background())
		if err != nil {
			return err
		}
		return ErrHostNotConfigured
	}
	managed.active = true
	h.mu.Unlock()

	if err := replay(ctx); err != nil {
		_ = h.unregister(context.Background(), id, managed)
		return err
	}
	return nil
}

func (h *Host) Unregister(ctx context.Context, id externalidentity.GatewayInstanceID) error {
	return h.unregister(ctx, id, nil)
}

func (h *Host) unregister(ctx context.Context, id externalidentity.GatewayInstanceID, expected *managedAdapter) error {
	h.mu.Lock()
	managed := h.adapters[id]
	if managed == nil || (expected != nil && managed != expected) {
		h.mu.Unlock()
		return ErrUnavailable
	}
	delete(h.adapters, id)
	managed.active = false
	h.mu.Unlock()
	<-managed.startDone
	return managed.stop(ctx)
}

func (h *Host) DeliverSessionEvent(ctx context.Context, owner session.Owner, delivery sessionevent.Delivery) error {
	if owner.Kind != session.OwnerGateway {
		return sessionevent.TerminalDeliveryError(ErrUnsupportedOwner)
	}
	h.mu.Lock()
	managed := h.adapters[externalidentity.GatewayInstanceID(owner.InstanceID)]
	if managed == nil || !managed.active {
		h.mu.Unlock()
		return ErrUnavailable
	}
	if managed.version < StructuredSessionEventVersion {
		h.mu.Unlock()
		return sessionevent.TerminalDeliveryError(ErrUnsupportedABI)
	}
	managed.deliveries.Add(1)
	h.mu.Unlock()
	defer managed.deliveries.Done()
	envelope, err := NewOutboundEnvelope(delivery)
	if err != nil {
		return sessionevent.TerminalDeliveryError(err)
	}
	return managed.adapter.Deliver(ctx, envelope)
}

func (h *Host) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	managed := make([]*managedAdapter, 0, len(h.adapters))
	for id, adapter := range h.adapters {
		adapter.active = false
		managed = append(managed, adapter)
		delete(h.adapters, id)
	}
	h.mu.Unlock()

	var first error
	for _, adapter := range managed {
		<-adapter.startDone
		if err := adapter.stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var _ sessionevent.Transport = (*Host)(nil)
