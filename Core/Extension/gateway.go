package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const pendingInboundKey = "gateway/pending-inbound"

type routingIngress interface {
	ResolvePlatformSession(context.Context, string) (session.ID, error)
	ExternalConversationRef(context.Context, session.ID) (string, error)
}
type deliveryCommand struct {
	ctx      context.Context
	envelope gateway.OutboundEnvelope
	result   chan error
}
type deliveryResult struct {
	EventType  string `json:"event_type"`
	DeliveryID string `json:"delivery_id"`
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
}
type gatewayIncoming struct {
	EventType string                  `json:"event_type"`
	Source    string                  `json:"source"`
	UpdateID  int64                   `json:"update_id"`
	ChatID    int64                   `json:"chat_id"`
	SenderID  int64                   `json:"sender_id"`
	Text      string                  `json:"text"`
	Media     []events.MediaReference `json:"media,omitempty"`
}

type GatewayAdapter struct {
	manifest  Manifest
	runtime   *WASMRuntime
	ingress   gateway.Ingress
	routes    routingIngress
	cancel    context.CancelFunc
	done      chan struct{}
	commands  chan deliveryCommand
	mu        sync.Mutex
	lastError error
	pending   []GuestMessage
}

func NewGatewayAdapter(manifest Manifest, runtime *WASMRuntime) *GatewayAdapter {
	return &GatewayAdapter{manifest: manifest, runtime: runtime, commands: make(chan deliveryCommand)}
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
	if err := a.restorePending(context.Background()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.done = make(chan struct{})
	go a.run(ctx)
	return nil
}

func (a *GatewayAdapter) restorePending(ctx context.Context) error {
	data, err := a.runtime.state.Get(ctx, a.manifest.InstanceID, pendingInboundKey)
	if errors.Is(err, ErrStateNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 0 && json.Unmarshal(data, &a.pending) != nil {
		return ErrInvalidManifest
	}
	return nil
}
func (a *GatewayAdapter) LastError() error   { a.mu.Lock(); defer a.mu.Unlock(); return a.lastError }
func (a *GatewayAdapter) setError(err error) { a.mu.Lock(); a.lastError = err; a.mu.Unlock() }

// run is the sole consumer of guest output. Poll and delivery calls may both
// cause guest emissions, but only this dispatcher drains and classifies them.
func (a *GatewayAdapter) run(ctx context.Context) {
	defer close(a.done)
	poll := time.NewTicker(100 * time.Millisecond)
	retry := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case command := <-a.commands:
			command.result <- a.executeDelivery(command.ctx, command.envelope)
		case <-retry.C:
			a.retryInbound(ctx)
		case <-poll.C:
			if len(a.pending) > 0 {
				continue
			}
			if err := a.runtime.Poll(a.manifest.InstanceID); err != nil {
				a.setError(err)
				continue
			}
			a.dispatch(ctx, a.runtime.TakeOutbound(a.manifest.InstanceID), nil)
			a.retryInbound(ctx)
		}
	}
}

func (a *GatewayAdapter) dispatch(ctx context.Context, messages []GuestMessage, results map[string]deliveryResult) {
	for _, message := range messages {
		var result deliveryResult
		if message.Type == "outgoing" && json.Unmarshal(message.Payload, &result) == nil && result.EventType == "delivery.result" {
			if results != nil && result.DeliveryID != "" {
				results[result.DeliveryID] = result
			}
			continue
		}
		var incoming gatewayIncoming
		if message.Type != "outgoing" || json.Unmarshal(message.Payload, &incoming) != nil || incoming.EventType != "message.incoming" {
			continue
		}
		id := a.inboundID(incoming)
		if _, err := a.runtime.state.Get(ctx, a.manifest.InstanceID, "gateway/inbound-accepted/"+id); err == nil {
			a.ackInbound(ctx, id)
			continue
		}
		if !a.pendingContains(id) {
			a.pending = append(a.pending, message)
			if err := a.persistPending(ctx); err != nil {
				a.setError(err)
			}
		}
	}
}
func (a *GatewayAdapter) pendingContains(id string) bool {
	for _, message := range a.pending {
		var in gatewayIncoming
		_ = json.Unmarshal(message.Payload, &in)
		if a.inboundID(in) == id {
			return true
		}
	}
	return false
}
func (a *GatewayAdapter) persistPending(ctx context.Context) error {
	data, err := json.Marshal(a.pending)
	if err != nil {
		return err
	}
	return a.runtime.state.Put(ctx, a.manifest.InstanceID, pendingInboundKey, data)
}
func (a *GatewayAdapter) retryInbound(ctx context.Context) {
	if len(a.pending) == 0 {
		return
	}
	// An event must be recoverable before it crosses the Core ingress
	// boundary. This also retries a failed state write without polling the
	// guest again or advancing its cursor.
	if err := a.persistPending(ctx); err != nil {
		a.setError(err)
		return
	}
	message := a.pending[0]
	var in gatewayIncoming
	if err := json.Unmarshal(message.Payload, &in); err != nil {
		a.setError(err)
		return
	}
	id := a.inboundID(in)
	if _, err := a.runtime.state.Get(ctx, a.manifest.InstanceID, "gateway/inbound-accepted/"+id); err == nil {
		a.pending = a.pending[1:]
		if err = a.persistPending(ctx); err != nil {
			a.setError(err)
			return
		}
		a.ackInbound(ctx, id)
		return
	}
	if err := a.handleInbound(ctx, in); err != nil {
		a.setError(err)
		return
	}
	if err := a.runtime.state.Put(ctx, a.manifest.InstanceID, "gateway/inbound-accepted/"+id, []byte("accepted")); err != nil {
		a.setError(err)
		return
	}
	a.pending = a.pending[1:]
	if err := a.persistPending(ctx); err != nil {
		a.setError(err)
		return
	}
	a.ackInbound(ctx, id)
}

func (a *GatewayAdapter) ackInbound(ctx context.Context, id string) {
	ack, _ := json.Marshal(map[string]string{"event_id": id})
	if err := a.runtime.HandleEvent(a.manifest.InstanceID, "inbound.accepted", ack); err != nil {
		a.setError(err)
		return
	}
	a.setError(nil)
	a.dispatch(ctx, a.runtime.TakeOutbound(a.manifest.InstanceID), nil)
}
func (a *GatewayAdapter) inboundID(in gatewayIncoming) string {
	return strconv.FormatInt(in.UpdateID, 10)
}

func (a *GatewayAdapter) handleInbound(ctx context.Context, in gatewayIncoming) error {
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
	event := events.Event{
		ID:        a.manifest.InstanceID + ":" + a.inboundID(in),
		Type:      events.TypeMessage,
		Timestamp: time.Now().UTC(),
		Source:    "client",
		DollID:    a.manifest.Gateway.DollID,
		Payload:   events.MessagePayload{Text: in.Text, Media: append([]events.MediaReference(nil), in.Media...)},
	}
	_, err := a.ingress.HandlePlatformMessage(ctx, conversation, sender, &event)
	return err
}

func (a *GatewayAdapter) Deliver(ctx context.Context, envelope gateway.OutboundEnvelope) error {
	command := deliveryCommand{ctx: ctx, envelope: envelope, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return ErrInvalidManifest
	case a.commands <- command:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-command.result:
		return err
	}
}
func (a *GatewayAdapter) executeDelivery(ctx context.Context, envelope gateway.OutboundEnvelope) error {
	delivery, err := envelope.DecodeDelivery()
	if err != nil {
		return err
	}
	if _, err = a.runtime.state.Get(ctx, a.manifest.InstanceID, "delivery/"+delivery.ID); err == nil {
		return nil
	}
	conversation, err := a.routes.ExternalConversationRef(ctx, delivery.Event.SessionID)
	if err != nil {
		return err
	}
	conversationID := strings.TrimPrefix(conversation, a.manifest.Gateway.ConversationPrefix)
	routePayload := map[string]any{"conversation_ref": conversation, "conversation_id": conversationID, "delivery_id": delivery.ID}
	if numeric, e := strconv.ParseInt(conversationID, 10, 64); e == nil {
		routePayload["chat_id"] = numeric
	}
	route, _ := json.Marshal(routePayload)
	if err = a.runtime.HandleEvent(a.manifest.InstanceID, "session.route", route); err != nil {
		return err
	}
	a.dispatch(ctx, a.runtime.TakeOutbound(a.manifest.InstanceID), nil)
	if err = a.runtime.HandleEvent(a.manifest.InstanceID, "session.event", envelope.Payload); err != nil {
		return err
	}
	results := map[string]deliveryResult{}
	a.dispatch(ctx, a.runtime.TakeOutbound(a.manifest.InstanceID), results)
	result, ok := results[delivery.ID]
	if !ok {
		return fmt.Errorf("extension gateway: missing correlated delivery result %s", delivery.ID)
	}
	if !result.OK {
		return errors.New("extension gateway: " + result.Error)
	}
	return a.runtime.state.Put(ctx, a.manifest.InstanceID, "delivery/"+delivery.ID, []byte("accepted"))
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
