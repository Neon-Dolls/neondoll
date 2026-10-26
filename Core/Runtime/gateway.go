package runtime

import (
	"context"
	"errors"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

// GatewayRuntime is the daemon-lifetime composition of the managed Gateway
// host, durable SessionEvent delivery, and permission lifecycle.
type GatewayRuntime struct {
	host        *gateway.Host
	events      *sessionevent.Service
	permissions *permissionrequest.Manager
	bridge      *sessionevent.PermissionBridge
}

func NewGatewayRuntime(
	ctx context.Context,
	services *Services,
	eventStore sessionevent.Store,
	permissionStore permissionrequest.Store,
	principals externalidentity.PrincipalResolver,
	router interaction.SessionRouter,
	policy actionpolicy.Policy,
) (*GatewayRuntime, error) {
	if services == nil || eventStore == nil || permissionStore == nil || principals == nil || router == nil || policy == nil {
		return nil, errors.New("runtime: invalid Gateway dependencies")
	}
	participants, ok := services.Sessions().(session.ParticipantService)
	if !ok {
		return nil, errors.New("runtime: Session participant service unavailable")
	}
	host := gateway.NewHost()
	events, err := sessionevent.New(eventStore, services.Sessions(), participants, host)
	if err != nil {
		return nil, err
	}
	permissions := permissionrequest.New(permissionStore, policy)
	bridge, err := sessionevent.NewPermissionBridge(permissions, principals, services.Sessions(), participants, events)
	if err != nil {
		return nil, err
	}
	runtime := &GatewayRuntime{host: host, events: events, permissions: permissions, bridge: bridge}
	if err := host.Configure(func(id externalidentity.GatewayInstanceID) (*interaction.GatewayIngress, error) {
		return services.GatewayIngress(id, router, bridge)
	}, runtime.replay); err != nil {
		return nil, err
	}
	// Pending records are retained when no adapter is currently available.
	_, err = events.ReplayPending(ctx)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *GatewayRuntime) replay(ctx context.Context) error {
	if _, err := r.events.ReplayPending(ctx); err != nil {
		return err
	}
	pending, err := r.permissions.ListPending(ctx)
	if err != nil {
		return err
	}
	for _, request := range pending {
		delivery, presentErr := r.bridge.Present(ctx, request.ID)
		if presentErr != nil && delivery == nil && !errors.Is(presentErr, sessionevent.ErrRecipientUnavailable) {
			return presentErr
		}
	}
	return nil
}

func (r *GatewayRuntime) Register(ctx context.Context, adapter gateway.Adapter) error {
	return r.host.Register(ctx, adapter)
}

func (r *GatewayRuntime) Unregister(ctx context.Context, id externalidentity.GatewayInstanceID) error {
	return r.host.Unregister(ctx, id)
}

func (r *GatewayRuntime) CreatePermission(ctx context.Context, input permissionrequest.CreateRequest) (*permissionrequest.Request, *sessionevent.Delivery, error) {
	request, err := r.permissions.Create(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	delivery, presentErr := r.bridge.Present(ctx, request.ID)
	return request, delivery, presentErr
}

func (r *GatewayRuntime) PermissionRequests() *permissionrequest.Manager { return r.permissions }
func (r *GatewayRuntime) Events() *sessionevent.Service                  { return r.events }
func (r *GatewayRuntime) Shutdown(ctx context.Context) error             { return r.host.Shutdown(ctx) }
