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

// Host owns installed Gateway lifecycles and is the production SessionEvent
// transport for Gateway-owned Sessions.
type Host struct {
	mu       sync.RWMutex
	adapters map[externalidentity.GatewayInstanceID]Adapter
	ingress  IngressFactory
	replay   ReplayFunc
	closed   bool
}

func NewHost() *Host {
	return &Host{adapters: make(map[externalidentity.GatewayInstanceID]Adapter)}
}

// Configure installs Core-owned ingress and replay boundaries before any
// Gateway instance can start.
func (h *Host) Configure(ingress IngressFactory, replay ReplayFunc) error {
	if h == nil || ingress == nil || replay == nil {
		return ErrHostNotConfigured
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.adapters) != 0 {
		return ErrHostNotConfigured
	}
	h.ingress, h.replay = ingress, replay
	return nil
}

func (h *Host) Register(ctx context.Context, adapter Adapter) error {
	if h == nil || adapter == nil || strings.TrimSpace(string(adapter.InstanceID())) == "" ||
		(adapter.ABIVersion() != LegacyABIVersion && adapter.ABIVersion() != StructuredABIVersion) {
		return ErrInvalidAdapter
	}
	h.mu.Lock()
	if h.closed || h.ingress == nil || h.replay == nil {
		h.mu.Unlock()
		return ErrHostNotConfigured
	}
	id := adapter.InstanceID()
	if _, exists := h.adapters[id]; exists {
		h.mu.Unlock()
		return ErrAlreadyRegistered
	}
	factory, replay := h.ingress, h.replay
	h.mu.Unlock()

	ingress, err := factory(id)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx, &boundIngress{version: adapter.ABIVersion(), ingress: ingress, replay: replay}); err != nil {
		return err
	}

	h.mu.Lock()
	if h.closed || h.adapters[id] != nil {
		h.mu.Unlock()
		_ = adapter.Stop(context.Background())
		if h.closed {
			return ErrHostNotConfigured
		}
		return ErrAlreadyRegistered
	}
	h.adapters[id] = adapter
	h.mu.Unlock()

	// The adapter is visible before replay, so pending deliveries can resolve
	// this instance. Failures remain durable and retryable.
	if err := replay(ctx); err != nil {
		h.mu.Lock()
		delete(h.adapters, id)
		h.mu.Unlock()
		_ = adapter.Stop(context.Background())
		return err
	}
	return nil
}

func (h *Host) Unregister(ctx context.Context, id externalidentity.GatewayInstanceID) error {
	h.mu.Lock()
	adapter := h.adapters[id]
	delete(h.adapters, id)
	h.mu.Unlock()
	if adapter == nil {
		return ErrUnavailable
	}
	return adapter.Stop(ctx)
}

func (h *Host) DeliverSessionEvent(ctx context.Context, owner session.Owner, delivery sessionevent.Delivery) error {
	if owner.Kind != session.OwnerGateway {
		return sessionevent.TerminalDeliveryError(ErrUnsupportedOwner)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	adapter := h.adapters[externalidentity.GatewayInstanceID(owner.InstanceID)]
	if adapter == nil {
		return ErrUnavailable
	}
	if adapter.ABIVersion() < StructuredABIVersion {
		return sessionevent.TerminalDeliveryError(ErrUnsupportedABI)
	}
	return adapter.Deliver(ctx, OutboundEnvelope{ABIVersion: StructuredABIVersion, Delivery: delivery})
}

func (h *Host) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	adapters := make([]Adapter, 0, len(h.adapters))
	for id, adapter := range h.adapters {
		adapters = append(adapters, adapter)
		delete(h.adapters, id)
	}
	h.mu.Unlock()
	var first error
	for _, adapter := range adapters {
		if err := adapter.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var _ sessionevent.Transport = (*Host)(nil)
