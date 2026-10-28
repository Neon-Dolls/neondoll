package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

// GatewayRuntime is the daemon-lifetime composition of the managed Gateway
// host, durable SessionEvent delivery, and permission lifecycle.
type GatewayRuntime struct {
	host        *gateway.Host
	events      *sessionevent.Service
	permissions *permissionrequest.Manager
	bridge      *sessionevent.PermissionBridge
	replayMu    sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
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
	replayCtx, cancel := context.WithCancel(context.Background())
	runtime := &GatewayRuntime{host: host, events: events, permissions: permissions, bridge: bridge, cancel: cancel, done: make(chan struct{})}
	if err := host.Configure(func(id externalidentity.GatewayInstanceID) (*interaction.GatewayIngress, error) {
		return services.GatewayIngress(id, router, bridge)
	}, runtime.replay, gateway.WithSessionService(services.Sessions()), gateway.WithResponseEmitter(runtime.emitInteractionResponse)); err != nil {
		cancel()
		return nil, err
	}
	// Pending records are retained when no adapter is currently available.
	_, err = events.ReplayPending(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	go runtime.retryLoop(replayCtx)
	return runtime, nil
}

func (r *GatewayRuntime) replay(ctx context.Context) error {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
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

func (r *GatewayRuntime) retryLoop(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.replay(ctx)
		}
	}
}

func (r *GatewayRuntime) emitInteractionResponse(ctx context.Context, sessionID session.ID, response *events.Event) error {
	var typ sessionevent.Type
	var payload any
	switch response.Type {
	case events.TypeMessage:
		message, ok := response.Payload.(events.MessagePayload)
		if !ok {
			return errors.New("runtime: invalid interaction message response")
		}
		typ = sessionevent.TypeMessage
		payload = sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartText, Text: message.Text}}}
	case events.TypeSystem:
		system, ok := response.Payload.(events.SystemPayload)
		if !ok || system.Event != "error" {
			return errors.New("runtime: unsupported interaction system response")
		}
		encoded, _ := json.Marshal(system.Data)
		var message string
		if json.Unmarshal(encoded, &message) != nil || message == "" {
			message = "interaction failed"
		}
		typ = sessionevent.TypeError
		payload = sessionevent.ErrorPayload{Code: "interaction_error", Message: message}
	default:
		return fmt.Errorf("runtime: unsupported interaction response type %q", response.Type)
	}
	event, err := sessionevent.NewEvent(sessionID, typ, payload, sessionevent.Correlation{CausationEventID: response.CorrelationID}, time.Now())
	if err != nil {
		return err
	}
	delivery, deliverErr := r.events.Emit(ctx, event, "")
	if delivery != nil {
		// Persistence succeeded. Transient transport failure is retried without
		// repeating the admitted interaction or model invocation.
		return nil
	}
	return deliverErr
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
func (r *GatewayRuntime) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.host.Shutdown(ctx)
}
