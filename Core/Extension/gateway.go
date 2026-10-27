package extension

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type routingIngress interface {
	ResolvePlatformSession(context.Context, string) (session.ID, error)
	ExternalConversationRef(context.Context, session.ID) (string, error)
}

type GatewayAdapter struct {
	manifest Manifest
	runtime  *WASMRuntime
	ingress  gateway.Ingress
	routes   routingIngress
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
}

func NewGatewayAdapter(manifest Manifest, runtime *WASMRuntime) *GatewayAdapter {
	return &GatewayAdapter{manifest: manifest, runtime: runtime}
}
func (a *GatewayAdapter) InstanceID() externalidentity.GatewayInstanceID {
	return externalidentity.GatewayInstanceID(a.manifest.InstanceID)
}
func (a *GatewayAdapter) SessionEventVersion() int { return gateway.StructuredSessionEventVersion }
func (a *GatewayAdapter) Start(_ context.Context, ingress gateway.Ingress) error {
	routes, ok := ingress.(routingIngress)
	if !ok {
		return gateway.ErrHostNotConfigured
	}
	a.ingress, a.routes = ingress, routes
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.done = make(chan struct{})
	go a.poll(ctx)
	return nil
}
func (a *GatewayAdapter) poll(ctx context.Context) {
	defer close(a.done)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = a.runtime.Poll(a.manifest.InstanceID)
		for _, message := range a.runtime.TakeOutbound(a.manifest.InstanceID) {
			_ = a.handle(ctx, message)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type gatewayIncoming struct {
	EventType string `json:"event_type"`
	Source    string `json:"source"`
	UpdateID  int64  `json:"update_id"`
	ChatID    int64  `json:"chat_id"`
	SenderID  int64  `json:"sender_id"`
	Text      string `json:"text"`
}

func (a *GatewayAdapter) handle(ctx context.Context, message GuestMessage) error {
	var in gatewayIncoming
	if message.Type != "outgoing" || json.Unmarshal(message.Payload, &in) != nil || in.EventType != "message.incoming" {
		return nil
	}
	conversation := a.manifest.Gateway.ConversationPrefix + strconv.FormatInt(in.ChatID, 10)
	sender := gateway.VerifiedSender{Platform: a.manifest.Gateway.Platform, SubjectID: strconv.FormatInt(in.SenderID, 10)}
	fields := strings.Fields(in.Text)
	if len(fields) == 2 && (strings.EqualFold(fields[0], "/approve") || strings.EqualFold(fields[0], "/deny")) {
		sid, err := a.routes.ResolvePlatformSession(ctx, conversation)
		if err != nil {
			return err
		}
		decision := permissionrequest.DecisionApprove
		if strings.EqualFold(fields[0], "/deny") {
			decision = permissionrequest.DecisionDeny
		}
		event, err := sessionevent.NewEvent(sid, sessionevent.TypePermissionDecided, sessionevent.PermissionDecidedPayload{RequestID: fields[1], Decision: decision}, sessionevent.Correlation{RequestID: fields[1]}, time.Now())
		if err != nil {
			return err
		}
		_, err = a.ingress.HandleSessionEvent(ctx, sender, event)
		return err
	}
	event := events.NewDollMessage(a.manifest.InstanceID+":"+strconv.FormatInt(in.UpdateID, 10), a.manifest.Gateway.DollID, in.Text)
	_, err := a.ingress.HandlePlatformMessage(ctx, conversation, sender, &event)
	return err
}
func (a *GatewayAdapter) Deliver(ctx context.Context, envelope gateway.OutboundEnvelope) error {
	delivery, err := envelope.DecodeDelivery()
	if err != nil {
		return err
	}
	if _, err := a.runtime.state.Get(ctx, a.manifest.InstanceID, "delivery/"+delivery.ID); err == nil {
		return nil
	}
	conversation, err := a.routes.ExternalConversationRef(ctx, delivery.Event.SessionID)
	if err != nil {
		return err
	}
	conversationID := strings.TrimPrefix(conversation, a.manifest.Gateway.ConversationPrefix)
	routePayload := map[string]any{"conversation_ref": conversation, "conversation_id": conversationID, "delivery_id": delivery.ID}
	// ABI v2 compatibility alias used by the independently built Telegram guest.
	if numeric, parseErr := strconv.ParseInt(conversationID, 10, 64); parseErr == nil {
		routePayload["chat_id"] = numeric
	}
	route, _ := json.Marshal(routePayload)
	if err = a.runtime.HandleEvent(a.manifest.InstanceID, "session.route", route); err != nil {
		return err
	}
	if err = a.runtime.HandleEvent(a.manifest.InstanceID, "session.event", envelope.Payload); err != nil {
		return err
	}
	for _, message := range a.runtime.TakeOutbound(a.manifest.InstanceID) {
		var result struct {
			EventType string `json:"event_type"`
			OK        bool   `json:"ok"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(message.Payload, &result) == nil && result.EventType == "delivery.result" {
			if !result.OK {
				return errors.New("extension gateway: " + result.Error)
			}
			return a.runtime.state.Put(ctx, a.manifest.InstanceID, "delivery/"+delivery.ID, []byte("accepted"))
		}
	}
	return errors.New("extension gateway: missing delivery result")
}
func (a *GatewayAdapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.runtime.Unload(ctx, a.manifest.InstanceID)
}

var _ gateway.Adapter = (*GatewayAdapter)(nil)
