package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	actionpolicy "github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	body "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	dollmind "github.com/Neon-Dolls/neondoll/Core/DollMind"
	externalidentity "github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	coreruntime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

const (
	ScenarioConversation            = "conversation"
	ScenarioAuthorizedExecution     = "authorized-execution"
	ScenarioAutonomousCommunication = "autonomous-communication"
)

func Core5Profile() Profile {
	return Profile{Name: "core5", Scenarios: []Scenario{
		{ID: ScenarioConversation, Description: "trusted Gateway conversation reaches a correlated Interaction Session", Participants: []Participant{
			{Kind: "core", Mode: "real", Boundary: "production in-process runtime services"},
			{Kind: "gateway", Mode: "simulated", Boundary: "production Gateway.Adapter and Gateway.Ingress"},
			{Kind: "inference", Mode: "simulated", Boundary: "production inference.Provider"},
		}, Run: runConversation},
		{ID: ScenarioAuthorizedExecution, Description: "authorized cognition tool call executes through Body Guard", Participants: []Participant{
			{Kind: "core", Mode: "real", Boundary: "production CognitiveExecution, ToolExecutor, Registry, and Guard"},
			{Kind: "body", Mode: "simulated", Boundary: "production Body interface"},
			{Kind: "inference", Mode: "simulated", Boundary: "production inference.Provider"},
		}, Run: runAuthorizedExecution},
		{ID: ScenarioAutonomousCommunication, Description: "Pulse cognition discovers a trusted peer and emits a durable Gateway Session event", Participants: []Participant{
			{Kind: "core", Mode: "real", Boundary: "production Pulse/DollMind, communication, SessionEvent, and Gateway runtime"},
			{Kind: "gateway", Mode: "simulated", Boundary: "production Gateway.Adapter and Gateway.Ingress"},
			{Kind: "inference", Mode: "simulated", Boundary: "production inference.Provider"},
		}, Run: runAutonomousCommunication},
	}}
}

type scriptedProvider struct {
	mu        sync.Mutex
	responses []*inference.Response
	requests  []inference.Request
}

func (p *scriptedProvider) ID() inference.ProviderID { return "acceptance-deterministic" }
func (p *scriptedProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return nil, errors.New("acceptance inference: no scripted response")
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	copy := *response
	copy.ProviderID = p.ID()
	return &copy, nil
}

func acceptanceModel() config.ModelConfiguration {
	return config.ModelConfiguration{Name: "acceptance", Provider: "deterministic", Model: "acceptance-v1", Endpoint: "in-process://acceptance", Budget: inference.DefaultModelContextBudget()}
}

type simulatedGateway struct {
	id      externalidentity.GatewayInstanceID
	mu      sync.Mutex
	ingress gateway.Ingress
	events  []gateway.OutboundEnvelope
}

func (g *simulatedGateway) InstanceID() externalidentity.GatewayInstanceID { return g.id }
func (*simulatedGateway) SessionEventVersion() int                         { return gateway.StructuredSessionEventVersion }
func (g *simulatedGateway) Start(_ context.Context, ingress gateway.Ingress) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ingress = ingress
	return nil
}
func (g *simulatedGateway) Deliver(_ context.Context, event gateway.OutboundEnvelope) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, event)
	return nil
}
func (*simulatedGateway) Stop(context.Context) error { return nil }
func (g *simulatedGateway) endpoint() gateway.Ingress {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ingress
}
func (g *simulatedGateway) delivered() []gateway.OutboundEnvelope {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateway.OutboundEnvelope(nil), g.events...)
}

func runConversation(ctx context.Context) error {
	directory, err := os.MkdirTemp("", "neondoll-acceptance-conversation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	store, err := persistence.NewStore(filepath.Join(directory, "core.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{DollID: "acceptance-doll", CanonicalName: "Acceptance Doll"}
	if err = store.SaveDoll(ctx, &state); err != nil {
		return err
	}
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{{Identity: externalidentity.Identity{GatewayInstanceID: "acceptance-gateway", Platform: "acceptance", SubjectID: "owner-42"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}}})
	if err != nil {
		return err
	}
	services, err := coreruntime.NewServices(ctx, store.(session.Store), coreruntime.WithExternalIdentityResolver(resolver))
	if err != nil {
		return err
	}
	provider := &scriptedProvider{responses: []*inference.Response{{Content: "correlated response"}}}
	handler := interaction.New(store, provider, logger.New(logger.ErrorLevel, io.Discard), interaction.WithModelConfiguration(acceptanceModel()))
	router, err := services.InteractionRouter(handler)
	if err != nil {
		return err
	}
	runtime, err := coreruntime.NewGatewayRuntime(ctx, services, store.(sessionevent.Store), store.(permissionrequest.Store), resolver, router, actionpolicy.Default())
	if err != nil {
		return err
	}
	defer runtime.Shutdown(context.Background())
	adapter := &simulatedGateway{id: "acceptance-gateway"}
	if err = runtime.Register(ctx, adapter); err != nil {
		return err
	}
	event := events.NewDollMessage("acceptance-event-1", "acceptance-doll", "hello")
	response, err := adapter.endpoint().HandlePlatformMessage(ctx, "acceptance/conversation/1", gateway.VerifiedSender{Platform: "acceptance", SubjectID: "owner-42"}, &event)
	if err != nil {
		return err
	}
	if response == nil || response.SessionID == "" || response.Event == nil || response.Event.CorrelationID != event.ID {
		return fmt.Errorf("conversation response lost Session/event correlation: %#v", response)
	}
	resolved, err := services.Sessions().Lookup(ctx, response.SessionID)
	if err != nil {
		return err
	}
	if resolved.Owner.Kind != session.OwnerGateway || resolved.Owner.InstanceID != "acceptance-gateway" || resolved.ExternalConversationRef != "acceptance/conversation/1" {
		return fmt.Errorf("unexpected resolved Session: %#v", resolved)
	}
	participants := services.Sessions().(session.ParticipantService)
	bindings, err := participants.ListParticipants(ctx, response.SessionID)
	if err != nil || len(bindings) != 1 || bindings[0].PrincipalID != "owner" {
		return fmt.Errorf("trusted participant binding missing: %#v, %v", bindings, err)
	}
	return nil
}

type simulatedBody struct {
	id       body.BodyID
	caps     *body.CapabilityRegistry
	mu       sync.Mutex
	requests []body.ExecutionRequest
}

func newSimulatedBody() *simulatedBody {
	caps := body.NewCapabilityRegistry()
	_ = caps.Register(body.Capability{ID: "acceptance.echo", Operations: []string{"echo"}, Available: true})
	return &simulatedBody{id: "acceptance-body", caps: caps}
}
func (b *simulatedBody) ID() body.BodyID             { return b.id }
func (*simulatedBody) Kind() body.BodyKind           { return body.BodyKindRemote }
func (*simulatedBody) Name() string                  { return "acceptance simulated Body" }
func (b *simulatedBody) Describe() []body.Capability { return b.caps.All() }
func (b *simulatedBody) ResolveCapability(capability, operation string) error {
	return b.caps.Resolve(capability, operation)
}
func (b *simulatedBody) RegisterCapability(capability body.Capability) error {
	return b.caps.Register(capability)
}
func (b *simulatedBody) Execute(request body.ExecutionRequest) (*body.ExecutionResult, error) {
	b.mu.Lock()
	b.requests = append(b.requests, request)
	b.mu.Unlock()
	value, _ := request.Arguments["text"].(string)
	return &body.ExecutionResult{ExecutionID: request.ExecutionID, Status: body.StatusSuccess, Output: `{"echo":` + fmt.Sprintf("%q", value) + `}`}, nil
}

func runAuthorizedExecution(ctx context.Context) error {
	registry := body.NewRegistry()
	target := newSimulatedBody()
	if err := registry.Register(target); err != nil {
		return err
	}
	guard := body.NewGuard(registry, body.NewRuleEvaluator(body.AuthorityRule{Doll: "acceptance-doll", Body: target.ID(), Capability: "acceptance.echo", Operation: "echo", Decision: body.AuthorityAllow}))
	executor := dollmind.NewToolExecutor(registry, guard, actionpolicy.Default(), "acceptance-doll")
	provider := &scriptedProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "echo-call", Name: "acceptance.echo__echo", Arguments: map[string]any{"text": "observable"}}}},
		{Content: "Body returned observable"},
	}}
	result, err := cognitiveexecution.Execute(ctx, cognitiveexecution.Request{Provider: provider, ModelConfiguration: acceptanceModel(), Purpose: inference.PurposePlan, ToolExecutor: executor, CognitiveRunID: "acceptance-run", BuildContext: func(_ context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
		messages := []inference.Message{{Role: "system", Content: "Acceptance execution", Tools: state.Tools}}
		for _, exchange := range state.Exchanges {
			content, formatErr := cognitiveexecution.FormatToolExchange(exchange)
			if formatErr != nil {
				return nil, formatErr
			}
			call, toolResult := exchange.Call, exchange.Result
			messages = append(messages, inference.Message{Role: "tool", Content: content, ToolCall: &call, ToolResult: &toolResult})
		}
		return messages, nil
	}})
	if err != nil {
		return err
	}
	if result.Response.Content != "Body returned observable" || len(result.Exchanges) != 1 || result.Exchanges[0].Result.Status != inference.ToolResultSuccess {
		return fmt.Errorf("execution result did not reach cognition: %#v", result)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.requests) != 1 || target.requests[0].Arguments["text"] != "observable" {
		return fmt.Errorf("Body requests=%#v", target.requests)
	}
	return nil
}

type staticInteractionHandler struct{}

func (staticInteractionHandler) HandleInteraction(_ context.Context, _ *session.InteractionSession, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "admitted")
	return &response, nil
}

type acceptanceMind struct {
	state    *dollstate.DollState
	provider inference.Provider
}

func (m *acceptanceMind) Inference() inference.Provider { return m.provider }
func (m *acceptanceMind) State() *dollstate.DollState   { return m.state }
func (*acceptanceMind) Save() error                     { return nil }

func runAutonomousCommunication(ctx context.Context) error {
	directory, err := os.MkdirTemp("", "neondoll-acceptance-communication-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	store, err := persistence.NewStore(filepath.Join(directory, "core.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{{Identity: externalidentity.Identity{GatewayInstanceID: "acceptance-gateway", Platform: "acceptance", SubjectID: "owner-42"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}}})
	if err != nil {
		return err
	}
	services, err := coreruntime.NewServices(ctx, store.(session.Store), coreruntime.WithExternalIdentityResolver(resolver))
	if err != nil {
		return err
	}
	router, err := services.InteractionRouter(staticInteractionHandler{})
	if err != nil {
		return err
	}
	runtime, err := coreruntime.NewGatewayRuntime(ctx, services, store.(sessionevent.Store), store.(permissionrequest.Store), resolver, router, actionpolicy.Default())
	if err != nil {
		return err
	}
	defer runtime.Shutdown(context.Background())
	adapter := &simulatedGateway{id: "acceptance-gateway"}
	if err = runtime.Register(ctx, adapter); err != nil {
		return err
	}
	inbound := events.NewDollMessage("establish-route", "acceptance-doll", "hello")
	if _, err = adapter.endpoint().HandlePlatformMessage(ctx, "acceptance/conversation/owner", gateway.VerifiedSender{Platform: "acceptance", SubjectID: "owner-42"}, &inbound); err != nil {
		return err
	}
	participants := services.Sessions().(session.ParticipantService)
	communicationService, err := communication.New(resolver, services.Sessions(), participants, runtime.Events())
	if err != nil {
		return err
	}
	registry := body.NewRegistry()
	toolExecutor := dollmind.NewToolExecutor(registry, body.NewGuard(registry, body.NewRuleEvaluator()), actionpolicy.Default(), "acceptance-doll")
	toolExecutor.SetCommunicationSender(communicationService)
	provider := &scriptedProvider{responses: []*inference.Response{
		{Content: `{"summary":"share an update","matters":true,"reason":"acceptance wake"}`},
		{ToolCalls: []inference.ToolCall{{ID: "discover", Name: "core.communication__list_peers", Arguments: map[string]any{}}}},
		{ToolCalls: []inference.ToolCall{{ID: "send", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner", "text": "Autonomous acceptance message"}}}},
		{Content: `{"summary":"message queued","proposed_action":"wait","observations":[],"should_reorient":false,"request_future_cognition":false}`},
	}}
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{DollID: "acceptance-doll", CanonicalName: "Acceptance Doll"}
	mind := &acceptanceMind{state: &state, provider: provider}
	model := acceptanceModel()
	scheduler := dollmind.New(provider, logger.New(logger.ErrorLevel, io.Discard), mind, dollmind.WithModelConfiguration(model), dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model), dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), dollmind.WithToolExecutor(toolExecutor))
	if err = scheduler.EnterPulseWake(ctx, pulse.PulseWake{Pressure: .9, EffectivePressure: .8}); err != nil {
		return err
	}
	deliveries := adapter.delivered()
	if len(deliveries) != 1 {
		return fmt.Errorf("outbound deliveries=%d, want 1", len(deliveries))
	}
	delivery, err := deliveries[0].DecodeDelivery()
	if err != nil {
		return err
	}
	if delivery.TargetPrincipalID != "owner" || delivery.Event.Correlation.CognitiveRunID == "" {
		return fmt.Errorf("delivery correlation=%#v", delivery)
	}
	var payload sessionevent.MessagePayload
	if err = json.Unmarshal(delivery.Event.Payload, &payload); err != nil || len(payload.Parts) != 1 || payload.Parts[0].Text != "Autonomous acceptance message" {
		return fmt.Errorf("outbound payload=%s error=%v", delivery.Event.Payload, err)
	}
	return nil
}
