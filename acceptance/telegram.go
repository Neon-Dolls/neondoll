package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	actionpolicy "github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	body "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	dollmind "github.com/Neon-Dolls/neondoll/Core/DollMind"
	extension "github.com/Neon-Dolls/neondoll/Core/Extension"
	externalidentity "github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	permissionrequest "github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	coreruntime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	sessionevent "github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

const (
	TelegramGatewayRevision    = "962e09b396a389950ed2d926ba38efc4baf37dfd"
	ScenarioTelegramInbound    = "telegram-wasm-inbound-conversation"
	ScenarioTelegramOutbound   = "telegram-wasm-autonomous-outbound"
	ScenarioTelegramPermission = "telegram-wasm-permission-interaction"
)

type TelegramDriverConfig struct {
	SourceDir, Artifact, Revision, TinyGo string
}

// TelegramDriver builds and owns the independently compiled Telegram guest.
// It never substitutes a Go Gateway adapter when the source or toolchain is
// unavailable.
type TelegramDriver struct {
	config            TelegramDriverConfig
	once              sync.Once
	artifact, tempDir string
	err               error
}

func NewTelegramDriver(config TelegramDriverConfig) *TelegramDriver {
	if config.Revision == "" {
		config.Revision = TelegramGatewayRevision
	}
	if config.TinyGo == "" {
		config.TinyGo = "tinygo"
	}
	return &TelegramDriver{config: config}
}

func (d *TelegramDriver) Revision() string { return d.config.Revision }
func (d *TelegramDriver) Artifact(ctx context.Context) (string, error) {
	d.once.Do(func() { d.prepare(ctx) })
	return d.artifact, d.err
}
func (d *TelegramDriver) prepare(ctx context.Context) {
	if strings.TrimSpace(d.config.SourceDir) == "" {
		d.err = errors.New("telegram acceptance: pinned Gateway source checkout is required")
		return
	}
	head, err := commandOutput(ctx, d.config.SourceDir, "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != d.config.Revision {
		d.err = fmt.Errorf("telegram acceptance: checkout revision %q, want %q", strings.TrimSpace(head), d.config.Revision)
		return
	}
	dirty, err := commandOutput(ctx, d.config.SourceDir, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil || strings.TrimSpace(dirty) != "" {
		d.err = errors.New("telegram acceptance: pinned Gateway checkout has uncommitted tracked changes")
		return
	}
	if d.config.Artifact != "" {
		if _, err = os.Stat(d.config.Artifact); err != nil {
			d.err = fmt.Errorf("telegram acceptance: artifact: %w", err)
			return
		}
		d.artifact = d.config.Artifact
		return
	}
	d.tempDir, err = os.MkdirTemp("", "neondoll-telegram-wasm-")
	if err != nil {
		d.err = err
		return
	}
	d.artifact = filepath.Join(d.tempDir, "gateway.wasm")
	cmd := exec.CommandContext(ctx, d.config.TinyGo, "build", "-target=wasi", "-opt=z", "-no-debug", "-o", d.artifact, "wasm/gateway/main.go")
	cmd.Dir = d.config.SourceDir
	output, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		d.err = fmt.Errorf("telegram acceptance: TinyGo build failed: %w: %s", buildErr, boundedDiagnostic(output))
	}
}
func (d *TelegramDriver) Close() {
	if d.tempDir != "" {
		_ = os.RemoveAll(d.tempDir)
	}
}

func Core5TelegramProfile(driver *TelegramDriver) Profile {
	base := Core5Profile()
	wasm := Participant{Kind: "gateway", Mode: "real-wasm", Revision: driver.Revision(), Boundary: "production Wazero Extension Manager and Gateway ABI v2"}
	telegram := Participant{Kind: "telegram-platform", Mode: "simulated", Boundary: "local TLS Telegram Bot API protocol fixture"}
	core := Participant{Kind: "core", Mode: "real-in-process", Boundary: "production Gateway, identity, Session, cognition, and SessionEvent services"}
	base.Scenarios = append(base.Scenarios,
		Scenario{ID: ScenarioTelegramInbound, Description: "real Telegram WASM admits a trusted conversation and returns a Session event", Participants: []Participant{core, wasm, telegram, {Kind: "inference", Mode: "simulated", Boundary: "production inference.Provider"}}, Run: func(ctx context.Context) error {
			return runBoundedTelegram(ctx, func(ctx context.Context) error { return runTelegramInbound(ctx, driver) })
		}},
		Scenario{ID: ScenarioTelegramOutbound, Description: "autonomous cognition discovers a peer and sends through real Telegram WASM", Participants: []Participant{core, wasm, telegram, {Kind: "inference", Mode: "simulated", Boundary: "production shared CognitiveExecution"}}, Run: func(ctx context.Context) error {
			return runBoundedTelegram(ctx, func(ctx context.Context) error { return runTelegramOutbound(ctx, driver) })
		}},
		Scenario{ID: ScenarioTelegramPermission, Description: "Owner permission is presented and decided through real Telegram WASM", Participants: []Participant{core, wasm, telegram, {Kind: "body", Mode: "simulated", Boundary: "production Body Guard and permission continuation"}}, Run: func(ctx context.Context) error {
			return runBoundedTelegram(ctx, func(ctx context.Context) error { return runTelegramPermission(ctx, driver) })
		}},
	)
	return base
}

func runBoundedTelegram(parent context.Context, run func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	return run(ctx)
}

type telegramUpdate struct {
	UpdateID, MessageID, SenderID, ChatID int64
	Text                                  string
}
type telegramSend struct {
	ChatID int64
	Text   string
}
type telegramAPI struct {
	server    *httptest.Server
	mu        sync.Mutex
	updates   []telegramUpdate
	sends     []telegramSend
	failSends int
}

func newTelegramAPI() *telegramAPI {
	a := &telegramAPI{}
	a.server = httptest.NewTLSServer(http.HandlerFunc(a.serveHTTP))
	return a
}
func (a *telegramAPI) Close() { a.server.Close() }
func (a *telegramAPI) Hostname() string {
	parsed, _ := url.Parse(a.server.URL)
	return parsed.Hostname()
}
func (a *telegramAPI) Add(update telegramUpdate) {
	a.mu.Lock()
	a.updates = append(a.updates, update)
	a.mu.Unlock()
}
func (a *telegramAPI) Sends() []telegramSend {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]telegramSend(nil), a.sends...)
}
func (a *telegramAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/getUpdates") {
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		a.mu.Lock()
		updates := append([]telegramUpdate(nil), a.updates...)
		a.mu.Unlock()
		result := make([]map[string]any, 0)
		for _, update := range updates {
			if update.UpdateID < offset {
				continue
			}
			result = append(result, map[string]any{"update_id": update.UpdateID, "message": map[string]any{"message_id": update.MessageID, "from": map[string]any{"id": update.SenderID, "is_bot": false}, "chat": map[string]any{"id": update.ChatID, "type": "private"}, "date": 1, "text": update.Text}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		chat, _ := strconv.ParseInt(r.URL.Query().Get("chat_id"), 10, 64)
		text := r.URL.Query().Get("text")
		a.mu.Lock()
		if a.failSends > 0 {
			a.failSends--
			a.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "temporary"})
			return
		}
		a.sends = append(a.sends, telegramSend{ChatID: chat, Text: text})
		id := len(a.sends)
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": id}})
		return
	}
	http.NotFound(w, r)
}

type telegramFixture struct {
	root       string
	store      persistence.Store
	services   *coreruntime.Services
	runtime    *coreruntime.GatewayRuntime
	extensions *extension.Manager
	api        *telegramAPI
	resolver   *externalidentity.MemoryResolver
}
type telegramFixtureOptions struct {
	handler    interaction.SessionHandler
	policy     actionpolicy.Policy
	resolution permissionrequest.ResolutionHandler
	requests   *permissionrequest.Manager
	mappings   []externalidentity.Mapping
}

func newTelegramFixture(ctx context.Context, driver *TelegramDriver, options telegramFixtureOptions) (*telegramFixture, error) {
	artifactPath, err := driver.Artifact(ctx)
	if err != nil {
		return nil, err
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "neondoll-telegram-acceptance-")
	if err != nil {
		return nil, err
	}
	f := &telegramFixture{root: root, api: newTelegramAPI()}
	fail := func(err error) (*telegramFixture, error) { f.Close(); return nil, err }
	store, err := persistence.NewStore(filepath.Join(root, "core.db"))
	if err != nil {
		return fail(err)
	}
	f.store = store
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{DollID: "acceptance-doll", CanonicalName: "Acceptance Doll"}
	state.Soul.Content = "Answer deterministically."
	if err = store.SaveDoll(ctx, &state); err != nil {
		return fail(err)
	}
	resolver, err := externalidentity.NewMemoryResolver(options.mappings)
	if err != nil {
		return fail(err)
	}
	f.resolver = resolver
	services, err := coreruntime.NewServices(ctx, store.(session.Store), coreruntime.WithExternalIdentityResolver(resolver))
	if err != nil {
		return fail(err)
	}
	f.services = services
	router, err := services.InteractionRouter(options.handler)
	if err != nil {
		return fail(err)
	}
	policy := options.policy
	if policy == nil {
		policy = actionpolicy.Default()
	}
	runtimeOptions := []coreruntime.GatewayRuntimeOption{}
	if options.requests != nil {
		runtimeOptions = append(runtimeOptions, coreruntime.WithPermissionManager(options.requests))
	}
	if options.resolution != nil {
		runtimeOptions = append(runtimeOptions, coreruntime.WithPermissionResolutionHandler(options.resolution))
	}
	gatewayRuntime, err := coreruntime.NewGatewayRuntime(ctx, services, store.(sessionevent.Store), store.(permissionrequest.Store), resolver, router, policy, runtimeOptions...)
	if err != nil {
		return fail(err)
	}
	f.runtime = gatewayRuntime
	dataRoot, secretsRoot := filepath.Join(root, "data"), filepath.Join(root, "secrets")
	if err = os.MkdirAll(dataRoot, 0700); err != nil {
		return fail(err)
	}
	if err = os.MkdirAll(secretsRoot, 0700); err != nil {
		return fail(err)
	}
	artifactCopy := filepath.Join(dataRoot, "telegram.wasm")
	if err = os.WriteFile(artifactCopy, artifact, 0600); err != nil {
		return fail(err)
	}
	if err = os.WriteFile(filepath.Join(secretsRoot, "token"), []byte("123:acceptance"), 0600); err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(artifact)
	configBytes, _ := json.Marshal(map[string]any{"version": 1, "telegram": map[string]any{"host": f.api.server.URL}, "polling": map[string]any{"timeout_seconds": 1, "allowed_updates": []string{"message"}}})
	manifest := extension.Manifest{Version: 1, ExtensionID: "telegram", InstanceID: "telegram-acceptance", Contract: extension.ContractGateway, ABI: 2, Enabled: true, Artifact: extension.Artifact{Path: "telegram.wasm", SHA256: fmt.Sprintf("%x", sum)}, Configuration: configBytes, Requested: []extension.Capability{extension.CapabilityHTTP, extension.CapabilitySecrets, extension.CapabilityState, extension.CapabilityLog}, Granted: []extension.Capability{extension.CapabilityHTTP, extension.CapabilitySecrets, extension.CapabilityState, extension.CapabilityLog}, HTTPAllowlist: []string{f.api.Hostname()}, Secrets: map[string]string{"telegram_token": "token"}, Gateway: &extension.GatewayConfiguration{Platform: "telegram", DollID: "acceptance-doll", ConversationPrefix: "telegram/chat/"}}
	wasmRuntime, err := extension.NewWASMRuntime(ctx, extension.NewFileStateStore(filepath.Join(root, "extension-state")), secretsRoot, extension.WithHTTPClient(f.api.server.Client()))
	if err != nil {
		return fail(err)
	}
	manager, err := extension.NewManager(wasmRuntime, gatewayRuntime, dataRoot, []extension.Manifest{manifest})
	if err != nil {
		_ = wasmRuntime.Close(context.Background())
		return fail(err)
	}
	f.extensions = manager
	if err = manager.LoadEnabled(ctx); err != nil {
		return fail(err)
	}
	return f, nil
}
func (f *telegramFixture) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if f.extensions != nil {
		_ = f.extensions.Shutdown(ctx)
	}
	if f.runtime != nil {
		_ = f.runtime.Shutdown(ctx)
	}
	if f.store != nil {
		_ = f.store.Close()
	}
	if f.api != nil {
		f.api.Close()
	}
	if f.root != "" {
		_ = os.RemoveAll(f.root)
	}
}
func ownerMappings() []externalidentity.Mapping {
	return []externalidentity.Mapping{{Identity: externalidentity.Identity{GatewayInstanceID: "telegram-acceptance", Platform: "telegram", SubjectID: "9"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}}}
}
func waitFor(ctx context.Context, description string, condition func() bool) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", description, ctx.Err())
		case <-ticker.C:
		}
	}
}

func runTelegramInbound(ctx context.Context, driver *TelegramDriver) error {
	provider := &scriptedProvider{responses: []*inference.Response{{Content: "hello from the Doll"}}}
	return withTelegramInteractionFixture(ctx, driver, provider, func(f *telegramFixture) error {
		// The first Telegram rejection must leave Core's stable delivery pending;
		// the production replay loop retries it without repeating inference.
		f.api.mu.Lock()
		f.api.failSends = 1
		f.api.mu.Unlock()
		f.api.Add(telegramUpdate{UpdateID: 42, MessageID: 1, SenderID: 9, ChatID: 7, Text: "hello Doll"})
		if err := waitFor(ctx, "Telegram response", func() bool { return len(f.api.Sends()) == 1 }); err != nil {
			return err
		}
		active, err := f.services.Sessions().ListActive(ctx, nil)
		if err != nil || len(active) != 1 {
			return fmt.Errorf("active Sessions=%d: %v", len(active), err)
		}
		if active[0].ExternalConversationRef != "telegram/chat/7" {
			return fmt.Errorf("conversation=%q", active[0].ExternalConversationRef)
		}
		history, err := f.store.(contextbuilder.HistoryStore).ListSessionMessages(ctx, active[0].ID)
		if err != nil || len(history) != 2 || history[0].Content != "hello Doll" || history[1].Content != "hello from the Doll" {
			return fmt.Errorf("history=%#v: %v", history, err)
		}
		bindings, err := f.services.Sessions().(session.ParticipantService).ListParticipants(ctx, active[0].ID)
		if err != nil || len(bindings) != 1 || bindings[0].PrincipalID != "owner" {
			return fmt.Errorf("participants=%#v: %v", bindings, err)
		}
		send := f.api.Sends()[0]
		if send.ChatID != 7 || send.Text != "hello from the Doll" {
			return fmt.Errorf("Telegram send=%#v", send)
		}
		return nil
	})
}

func withTelegramInteractionFixture(ctx context.Context, driver *TelegramDriver, provider inference.Provider, run func(*telegramFixture) error) error {
	// The handler and Store must share one persistence instance, so this helper
	// constructs the fixture in two explicit production composition steps.
	artifactPath, err := driver.Artifact(ctx)
	if err != nil {
		return err
	}
	_ = artifactPath
	return newCustomTelegramFixture(ctx, driver, ownerMappings(), actionpolicy.Default(), nil, nil, func(store persistence.Store) interaction.SessionHandler {
		return interaction.New(store, provider, logger.New(logger.ErrorLevel, io.Discard), interaction.WithModelConfiguration(acceptanceModel()))
	}, run)
}

func newCustomTelegramFixture(ctx context.Context, driver *TelegramDriver, mappings []externalidentity.Mapping, policy actionpolicy.Policy, requests *permissionrequest.Manager, resolution permissionrequest.ResolutionHandler, handlerFactory func(persistence.Store) interaction.SessionHandler, run func(*telegramFixture) error) error {
	// newTelegramFixture owns Store construction; this small bootstrap handler
	// is replaced by a Store-aware router by recreating the composition here.
	// Use a forwarding handler whose target is installed before polling starts.
	forward := &forwardInteractionHandler{}
	f, err := newTelegramFixture(ctx, driver, telegramFixtureOptions{handler: forward, policy: policy, requests: requests, resolution: resolution, mappings: mappings})
	if err != nil {
		return err
	}
	defer f.Close()
	forward.handler = handlerFactory(f.store)
	return run(f)
}

type forwardInteractionHandler struct{ handler interaction.SessionHandler }

func (f *forwardInteractionHandler) HandleInteraction(ctx context.Context, s *session.InteractionSession, e *events.Event) (*events.Event, error) {
	if f.handler == nil {
		return nil, errors.New("acceptance: interaction handler unavailable")
	}
	return f.handler.HandleInteraction(ctx, s, e)
}

func runTelegramOutbound(ctx context.Context, driver *TelegramDriver) error {
	return newCustomTelegramFixture(ctx, driver, ownerMappings(), actionpolicy.Default(), nil, nil, func(persistence.Store) interaction.SessionHandler { return silentInteractionHandler{} }, func(f *telegramFixture) error {
		f.api.Add(telegramUpdate{UpdateID: 50, MessageID: 1, SenderID: 9, ChatID: 8, Text: "establish trusted route"})
		if err := waitFor(ctx, "trusted Telegram Session", func() bool { active, _ := f.services.Sessions().ListActive(ctx, nil); return len(active) == 1 }); err != nil {
			return err
		}
		participants := f.services.Sessions().(session.ParticipantService)
		sender, err := communication.New(f.resolver, f.services.Sessions(), participants, f.runtime.Events())
		if err != nil {
			return err
		}
		registry := body.NewRegistry()
		executor := dollmind.NewToolExecutor(registry, body.NewGuard(registry, body.NewRuleEvaluator()), actionpolicy.Default(), "acceptance-doll")
		executor.SetCommunicationSender(sender)
		provider := &scriptedProvider{responses: []*inference.Response{{Content: `{"summary":"contact Owner","matters":true,"reason":"acceptance"}`}, {ToolCalls: []inference.ToolCall{{ID: "discover", Name: "core.communication__list_peers", Arguments: map[string]any{}}}}, {ToolCalls: []inference.ToolCall{{ID: "send", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner", "text": "Autonomous Telegram acceptance"}}}}, {Content: `{"summary":"queued","proposed_action":"wait","observations":[],"should_reorient":false,"request_future_cognition":false}`}}}
		state := dollstate.NewDollState()
		state.Identity = dollstate.Identity{DollID: "acceptance-doll", CanonicalName: "Acceptance Doll"}
		mind := &acceptanceMind{state: &state, provider: provider}
		model := acceptanceModel()
		scheduler := dollmind.New(provider, logger.New(logger.ErrorLevel, io.Discard), mind, dollmind.WithModelConfiguration(model), dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model), dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), dollmind.WithToolExecutor(executor))
		before := len(f.api.Sends())
		if err = scheduler.EnterPulseWake(ctx, pulse.PulseWake{Pressure: .9, EffectivePressure: .8}); err != nil {
			return err
		}
		if err = waitFor(ctx, "autonomous Telegram delivery", func() bool { return len(f.api.Sends()) > before }); err != nil {
			return err
		}
		send := f.api.Sends()[before]
		if send.ChatID != 8 || send.Text != "Autonomous Telegram acceptance" {
			return fmt.Errorf("Telegram send=%#v", send)
		}
		return nil
	})
}

type silentInteractionHandler struct{}

func (silentInteractionHandler) HandleInteraction(context.Context, *session.InteractionSession, *events.Event) (*events.Event, error) {
	return nil, nil
}

type permissionResolutionProxy struct {
	mu     sync.Mutex
	target permissionrequest.ResolutionHandler
}

func (p *permissionResolutionProxy) HandlePermissionResolution(ctx context.Context, r *permissionrequest.Request) error {
	p.mu.Lock()
	target := p.target
	p.mu.Unlock()
	if target == nil {
		return errors.New("acceptance: resolution target unavailable")
	}
	return target.HandlePermissionResolution(ctx, r)
}

func runTelegramPermission(ctx context.Context, driver *TelegramDriver) error {
	if err := runTelegramPermissionCase(ctx, driver, false); err != nil {
		return fmt.Errorf("unauthorized decision: %w", err)
	}
	return runTelegramPermissionCase(ctx, driver, true)
}
func runTelegramPermissionCase(ctx context.Context, driver *TelegramDriver, authorized bool) error {
	proxy := &permissionResolutionProxy{}
	policy, err := actionpolicy.New([]actionpolicy.Rule{{Capability: "acceptance.echo", Operation: "echo", Decision: actionpolicy.DecisionRequireApproval, Message: "Allow acceptance echo?"}})
	if err != nil {
		return err
	}
	return newCustomTelegramFixture(ctx, driver, ownerMappings(), policy, nil, proxy, func(persistence.Store) interaction.SessionHandler { return silentInteractionHandler{} }, func(f *telegramFixture) error {
		// The runtime-created manager uses the same policy and durable Store.
		requests := f.runtime.PermissionRequests()
		registry := body.NewRegistry()
		target := newSimulatedBody()
		if err = registry.Register(target); err != nil {
			return err
		}
		guard := body.NewGuard(registry, body.NewRuleEvaluator(body.AuthorityRule{Doll: "acceptance-doll", Body: target.ID(), Capability: "acceptance.echo", Operation: "echo", Decision: body.AuthorityAllow}))
		executor := dollmind.NewToolExecutor(registry, guard, policy, "acceptance-doll", dollmind.WithPermissionRequests(requests, "owner", time.Minute))
		proxy.mu.Lock()
		proxy.target = executor
		proxy.mu.Unlock()
		f.api.Add(telegramUpdate{UpdateID: 60, MessageID: 1, SenderID: 9, ChatID: 9, Text: "establish Owner route"})
		if err = waitFor(ctx, "Owner Session", func() bool { active, _ := f.services.Sessions().ListActive(ctx, nil); return len(active) == 1 }); err != nil {
			return err
		}
		tool := &inference.Tool{Name: "acceptance.echo__echo", CapabilityID: "acceptance.echo", OperationID: "echo"}
		result := executor.ExecuteCorrelatedCall(ctx, cognitiveexecution.ToolCorrelation{CognitiveRunID: "telegram-permission-run"}, inference.ToolCall{ID: "permission-call", Name: tool.Name, Arguments: map[string]any{"text": "approved"}}, tool)
		if result.Error == nil || result.Error.PermissionRequestID == "" {
			return fmt.Errorf("permission request result=%#v", result)
		}
		requestID := result.Error.PermissionRequestID
		if err = waitFor(ctx, "permission presentation", func() bool {
			for _, s := range f.api.Sends() {
				if strings.Contains(s.Text, requestID) {
					return true
				}
			}
			return false
		}); err != nil {
			return err
		}
		sender := int64(10)
		if authorized {
			sender = 9
		}
		f.api.Add(telegramUpdate{UpdateID: 61, MessageID: 2, SenderID: sender, ChatID: 9, Text: "/approve " + requestID})
		if authorized {
			if err = waitFor(ctx, "approved continuation", func() bool { target.mu.Lock(); defer target.mu.Unlock(); return len(target.requests) == 1 }); err != nil {
				return err
			}
			request, err := requests.Get(ctx, permissionrequest.ID(requestID))
			if err != nil || request.Status != permissionrequest.StatusApproved {
				return fmt.Errorf("request=%#v: %v", request, err)
			}
		} else {
			time.Sleep(350 * time.Millisecond)
			request, err := requests.Get(ctx, permissionrequest.ID(requestID))
			if err != nil || request.Status != permissionrequest.StatusPending {
				return fmt.Errorf("unauthorized decision changed request=%#v: %v", request, err)
			}
			target.mu.Lock()
			count := len(target.requests)
			target.mu.Unlock()
			if count != 0 {
				return fmt.Errorf("unauthorized decision executed Body %d times", count)
			}
		}
		return nil
	})
}

func boundedDiagnostic(data []byte) string {
	const limit = 4096
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strings.TrimSpace(string(data))
}
