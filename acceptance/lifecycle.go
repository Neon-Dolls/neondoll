package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	actionpolicy "github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	body "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	dollmind "github.com/Neon-Dolls/neondoll/Core/DollMind"
	externalidentity "github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Gateway"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	ownercontrol "github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	permissionrequest "github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	coreruntime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	sessionevent "github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

const (
	ScenarioDurableDollIdentity       = "lifecycle-durable-doll-identity"
	ScenarioIntentionContinuity       = "lifecycle-intention-continuity"
	ScenarioPendingPermissionRecovery = "lifecycle-pending-permission-recovery"
	ScenarioCommunicationRecovery     = "lifecycle-undelivered-communication-recovery"
	ScenarioBodyCoreRestart           = "lifecycle-shellbody-core-restart"
)

type CoreDaemonDriver struct {
	repository       string
	once             sync.Once
	binary, buildDir string
	err              error
}

func NewCoreDaemonDriver(repository string) *CoreDaemonDriver {
	return &CoreDaemonDriver{repository: repository}
}
func (d *CoreDaemonDriver) Prepare(ctx context.Context) error {
	d.once.Do(func() {
		if d.repository == "" {
			d.err = errors.New("lifecycle acceptance: Core repository is required")
			return
		}
		var err error
		d.buildDir, err = os.MkdirTemp("", "neondoll-lifecycle-daemon-")
		if err != nil {
			d.err = err
			return
		}
		d.binary = filepath.Join(d.buildDir, "neondolld")
		build, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(build, "go", "build", "-tags=acceptance", "-o", d.binary, "./cmd/neondolld")
		cmd.Dir = d.repository
		if output, err := cmd.CombinedOutput(); err != nil {
			d.err = fmt.Errorf("build lifecycle daemon: %w: %s", err, tail(string(output), 4096))
		}
	})
	return d.err
}
func (d *CoreDaemonDriver) Close() {
	if d.buildDir != "" {
		_ = os.RemoveAll(d.buildDir)
	}
}

func Core5LifecycleProfile(daemon *CoreDaemonDriver, shell *ShellBodyDriver) Profile {
	daemonParticipant := Participant{Kind: "core", Mode: "real-process", Revision: CoreRevision(), Boundary: "independent neondolld process with production persistence and startup recovery"}
	inProcess := Participant{Kind: "core", Mode: "real-in-process", Revision: CoreRevision(), Boundary: "fresh production services over a closed and reopened durable store"}
	return Profile{Name: "core5", Scenarios: []Scenario{
		{ID: ScenarioDurableDollIdentity, Description: "Doll and Core identity survive a graceful daemon restart", Participants: []Participant{daemonParticipant}, Lifecycle: &LifecycleEvidence{RestartType: "graceful", ProcessBoundary: "neondolld OS process terminated and relaunched", RecoveredState: []string{"Core signing identity", "Doll identity and durable intention record"}, ObservableOutcome: "restarted daemon serves the same identity and owner-local control"}, Run: daemon.runIdentity},
		{ID: ScenarioIntentionContinuity, Description: "an unfinished intention is reconstructed and reconsidered", Participants: []Participant{inProcess, {Kind: "inference", Mode: "simulated", Boundary: "production shared cognition execution"}}, Lifecycle: &LifecycleEvidence{RestartType: "in-process", ProcessBoundary: "Store closed; DollMind, provider, and Scheduler discarded and reconstructed", RecoveredState: []string{"Doll identity", "pending intention"}, ObservableOutcome: "fresh Scheduler discovers and completes the original intention without duplication"}, Run: runIntentionRestart},
		{ID: ScenarioPendingPermissionRecovery, Description: "pending approval survives reconstruction and executes once", Participants: []Participant{inProcess, {Kind: "body", Mode: "simulated", Boundary: "production Registry, Guard, and permission continuation"}}, Lifecycle: &LifecycleEvidence{RestartType: "in-process", ProcessBoundary: "Store closed; Permission Manager and ToolExecutor discarded and reconstructed", RecoveredState: []string{"pending PermissionRequest", "action arguments and correlation"}, ObservableOutcome: "authenticated approval resumes guarded execution exactly once"}, Run: runPermissionRestart},
		{ID: ScenarioCommunicationRecovery, Description: "pending communication replays with stable identifiers", Participants: []Participant{inProcess, {Kind: "gateway", Mode: "simulated", Boundary: "production Gateway Host and SessionEvent transport"}}, Lifecycle: &LifecycleEvidence{RestartType: "in-process", ProcessBoundary: "Store closed; Session, Gateway runtime, and transport discarded and reconstructed", RecoveredState: []string{"Interaction Session", "participant binding", "pending SessionEvent delivery"}, ObservableOutcome: "replacement Gateway receives the original delivery and event IDs without rerunning inference"}, Run: runCommunicationRestart},
		{ID: ScenarioBodyCoreRestart, Description: "real Shell Body reconnects after a graceful daemon restart", Participants: []Participant{daemonParticipant, {Kind: "body", Mode: "real", Revision: shell.Revision(), Boundary: "independent neondoll-shellbody process over authenticated Doll Link"}}, Lifecycle: &LifecycleEvidence{RestartType: "graceful", ProcessBoundary: "neondolld OS process terminated while Shell Body remains independently running", RecoveredState: []string{"Core signing identity", "Body enrollment credential and membership", "Body process identity"}, ObservableOutcome: "same Body reconnects, advertises capabilities, and completes an authorized harmless action"}, Run: func(ctx context.Context) error { return runDaemonShellBodyRestart(ctx, daemon, shell) }},
	}}
}

type daemonInstance struct {
	driver                *CoreDaemonDriver
	root, address, socket string
	process               *lifecycleProcess
}
type lifecycleProcess struct {
	cmd    *exec.Cmd
	done   chan error
	output *boundedBuffer
	mu     sync.Mutex
	exited bool
}

func (d *CoreDaemonDriver) newInstance(ctx context.Context) (*daemonInstance, error) {
	if err := d.Prepare(ctx); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "neondoll-lifecycle-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.Defaults()
	cfg.Core.Profile = "lifecycle-doll"
	cfg.Core.Pulse.Enabled = false
	cfg.Paths.DataDir = filepath.Join(root, "data")
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Paths.SecretsDir = filepath.Join(root, "secrets")
	cfg.HTTP.Enabled = true
	cfg.HTTP.Listen = address
	cfg.Link.WebSocket.Listen = address
	if err = config.Save(filepath.Join(root, "config.json"), &cfg); err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	return &daemonInstance{driver: d, root: root, address: address, socket: filepath.Join(root, "acceptance.sock")}, nil
}
func (d *daemonInstance) start(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, d.driver.binary)
	cmd.Dir = d.root
	cmd.Env = append(os.Environ(), "NEONDOLL_ACCEPTANCE_SOCKET="+d.socket)
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	output := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return err
	}
	p := &lifecycleProcess{cmd: cmd, done: make(chan error, 1), output: output}
	d.process = p
	go func() { err := cmd.Wait(); p.mu.Lock(); p.exited = true; p.mu.Unlock(); p.done <- err }()
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return waitFor(ready, "lifecycle daemon readiness", func() bool {
		if p.isExited() {
			return false
		}
		if _, err := os.Stat(d.socket); err != nil {
			return false
		}
		request, _ := http.NewRequestWithContext(ready, http.MethodGet, "http://"+d.address+"/v1/core/identity", nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}
		return false
	})
}
func (p *lifecycleProcess) isExited() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.exited }
func (d *daemonInstance) stop(force bool) error {
	if d.process == nil || d.process.isExited() {
		return nil
	}
	if force {
		if runtime.GOOS != "windows" {
			_ = syscall.Kill(-d.process.cmd.Process.Pid, syscall.SIGKILL)
		} else {
			_ = d.process.cmd.Process.Kill()
		}
	} else {
		_ = d.process.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-d.process.done:
		d.process = nil
		if force {
			return nil
		}
		return err
	case <-time.After(8 * time.Second):
		_ = d.process.cmd.Process.Kill()
		return errors.New("lifecycle daemon did not stop")
	}
}
func (d *daemonInstance) close() { _ = d.stop(true); _ = os.RemoveAll(d.root) }
func (d *daemonInstance) identity(ctx context.Context) (string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+d.address+"/v1/core/identity", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var value struct {
		CoreID string `json:"core_id"`
	}
	if err = json.NewDecoder(response.Body).Decode(&value); err != nil {
		return "", err
	}
	return value.CoreID, nil
}

func (d *CoreDaemonDriver) runIdentity(ctx context.Context) error {
	instance, err := d.newInstance(ctx)
	if err != nil {
		return err
	}
	defer instance.close()
	if err = os.MkdirAll(filepath.Join(instance.root, "data"), 0o700); err != nil {
		return err
	}
	store, err := persistence.NewStore(filepath.Join(instance.root, "data", "neondoll.db"))
	if err != nil {
		return err
	}
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{DollID: "lifecycle-doll", CanonicalName: "Lifecycle Doll"}
	state.Soul.Content = "durable lifecycle marker"
	state.Intentions.Items = []dollstate.IntentionItem{{ID: "identity-marker", Subject: "survive restart", WakeTime: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), State: dollstate.IntentionStatePending}}
	if err = store.SaveDoll(ctx, &state); err != nil {
		store.Close()
		return err
	}
	store.Close()
	if err = instance.start(ctx); err != nil {
		return err
	}
	before, err := instance.identity(ctx)
	if err != nil {
		return err
	}
	if err = instance.stop(false); err != nil {
		return err
	}
	if err = instance.start(ctx); err != nil {
		return err
	}
	after, err := instance.identity(ctx)
	if err != nil {
		return err
	}
	if before == "" || after != before {
		return fmt.Errorf("Core identity changed %q -> %q", before, after)
	}
	if _, err = ownercontrol.NewClient(filepath.Join(instance.root, "data", "neondoll-admin.sock")).ListBodies(ctx); err != nil {
		return fmt.Errorf("restarted owner control: %w", err)
	}
	if err = instance.stop(false); err != nil {
		return err
	}
	reopened, err := persistence.NewStore(filepath.Join(instance.root, "data", "neondoll.db"))
	if err != nil {
		return err
	}
	defer reopened.Close()
	loaded, err := reopened.LoadDoll(ctx, "lifecycle-doll")
	if err != nil {
		return err
	}
	if loaded.Identity.DollID != "lifecycle-doll" || loaded.Soul.Content != "durable lifecycle marker" || len(loaded.Intentions.Items) != 1 {
		return fmt.Errorf("durable Doll state changed: %#v", loaded)
	}
	return nil
}

type persistentMind struct {
	state    *dollstate.DollState
	store    persistence.Store
	provider inference.Provider
}

func (m *persistentMind) Inference() inference.Provider { return m.provider }
func (m *persistentMind) State() *dollstate.DollState   { return m.state }
func (m *persistentMind) Save() error                   { return m.store.SaveDoll(context.Background(), m.state) }

func runIntentionRestart(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "neondoll-intention-restart-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "core.db")
	store, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{DollID: "lifecycle-doll", CanonicalName: "Lifecycle Doll"}
	if err = store.SaveDoll(ctx, &state); err != nil {
		return err
	}
	wake := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	provider := &scriptedProvider{responses: []*inference.Response{{Content: `{"summary":"schedule durable work","matters":true,"reason":"continuity"}`}, {Content: fmt.Sprintf(`{"summary":"scheduled","proposed_action":"wait","observations":[],"should_reorient":false,"request_future_cognition":true,"future_subject":"finish durable work","future_reason":"restart proof","future_wake_time":%q}`, wake)}}}
	mind := &persistentMind{state: &state, store: store, provider: provider}
	scheduler := dollmind.New(provider, logger.New(logger.ErrorLevel, io.Discard), mind, dollmind.WithModelConfiguration(acceptanceModel()), dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, provider, acceptanceModel()), dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, provider, acceptanceModel()))
	if err = scheduler.EnterPulseWake(ctx, pulse.PulseWake{Pressure: .9, EffectivePressure: .9}); err != nil {
		return err
	}
	if len(state.Intentions.Items) != 1 {
		return fmt.Errorf("materialized intentions=%d", len(state.Intentions.Items))
	}
	id := state.Intentions.Items[0].ID
	if err = store.Close(); err != nil {
		return err
	}
	for time.Now().Before(parseTime(wake)) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	reopened, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	loaded, err := reopened.LoadDoll(ctx, "lifecycle-doll")
	if err != nil {
		return err
	}
	provider2 := &scriptedProvider{responses: []*inference.Response{{Content: `{"summary":"finish work","matters":true,"reason":"due"}`}, {Content: `{"summary":"complete","proposed_action":"done","observations":[],"should_reorient":false,"request_future_cognition":false}`}}}
	mind2 := &persistentMind{state: loaded, store: reopened, provider: provider2}
	scheduler2 := dollmind.New(provider2, logger.New(logger.ErrorLevel, io.Discard), mind2, dollmind.WithModelConfiguration(acceptanceModel()), dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, provider2, acceptanceModel()), dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, provider2, acceptanceModel()))
	due, err := scheduler2.DueIntentions()
	if err != nil || len(due) != 1 || due[0].ID != id {
		return fmt.Errorf("reconstructed due intentions=%#v: %v", due, err)
	}
	if _, err = scheduler2.EnterWake(ctx, events.IntentionWakePayload{IntentionID: due[0].ID, Subject: due[0].Subject, Description: due[0].Description}); err != nil {
		return err
	}
	if err = reopened.Close(); err != nil {
		return err
	}
	final, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	defer final.Close()
	settled, err := final.LoadDoll(ctx, "lifecycle-doll")
	if err != nil {
		return err
	}
	if len(settled.Intentions.Items) != 1 || settled.Intentions.Items[0].ID != id || settled.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		return fmt.Errorf("settled intentions=%#v", settled.Intentions.Items)
	}
	return nil
}
func parseTime(value string) time.Time { parsed, _ := time.Parse(time.RFC3339, value); return parsed }

type lifecycleOwnerBoundary struct {
	owner      string
	authorized bool
}

func (b lifecycleOwnerBoundary) AuthenticateOwner(context.Context) (permissionrequest.AuthenticatedOwner, error) {
	if b.owner == "" {
		return permissionrequest.AuthenticatedOwner{}, permissionrequest.ErrUnauthenticated
	}
	return permissionrequest.AuthenticatedOwner{ID: b.owner}, nil
}
func (b lifecycleOwnerBoundary) AuthorizeDoll(context.Context, permissionrequest.AuthenticatedOwner, string) error {
	if !b.authorized {
		return permissionrequest.ErrUnauthorized
	}
	return nil
}

func runPermissionRestart(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "neondoll-permission-restart-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "core.db")
	policy, _ := actionpolicy.New([]actionpolicy.Rule{{Capability: "acceptance.echo", Operation: "echo", Decision: actionpolicy.DecisionRequireApproval, Message: "approve durable action"}})
	store, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	registry := body.NewRegistry()
	target := newSimulatedBody()
	if err = registry.Register(target); err != nil {
		return err
	}
	guard := body.NewGuard(registry, body.NewRuleEvaluator(body.AuthorityRule{Doll: "lifecycle-doll", Body: target.ID(), Capability: "acceptance.echo", Operation: "echo", Decision: body.AuthorityAllow}))
	executor := dollmind.NewToolExecutor(registry, guard, policy, "lifecycle-doll", dollmind.WithPermissionRequests(requests, "owner", time.Hour))
	tool := &inference.Tool{Name: "acceptance.echo__echo", CapabilityID: "acceptance.echo", OperationID: "echo"}
	result := executor.ExecuteCorrelatedCall(ctx, cognitiveexecution.ToolCorrelation{CognitiveRunID: "before-restart", IntentionID: "intention-1"}, inference.ToolCall{ID: "permission-call", Name: tool.Name, Arguments: map[string]any{"text": "durable"}}, tool)
	if result.Error == nil || result.Error.PermissionRequestID == "" {
		return fmt.Errorf("permission result=%#v", result)
	}
	id := permissionrequest.ID(result.Error.PermissionRequestID)
	if err = store.Close(); err != nil {
		return err
	}
	reopened, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	defer reopened.Close()
	requests2 := permissionrequest.New(reopened.(permissionrequest.Store), policy)
	pending, err := requests2.Get(ctx, id)
	if err != nil || pending.Status != permissionrequest.StatusPending {
		return fmt.Errorf("recovered permission=%#v: %v", pending, err)
	}
	unauthorized := permissionrequest.NewDecisionService(requests2, lifecycleOwnerBoundary{owner: "intruder", authorized: true})
	if _, err = unauthorized.Resolve(ctx, permissionrequest.DecisionRequest{RequestID: id, Decision: permissionrequest.DecisionApprove}); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		return fmt.Errorf("unauthorized approval error=%v", err)
	}
	decision := permissionrequest.NewDecisionService(requests2, lifecycleOwnerBoundary{owner: "owner", authorized: true})
	approved, err := decision.Resolve(ctx, permissionrequest.DecisionRequest{RequestID: id, Decision: permissionrequest.DecisionApprove})
	if err != nil {
		return err
	}
	registry2 := body.NewRegistry()
	target2 := newSimulatedBody()
	if err = registry2.Register(target2); err != nil {
		return err
	}
	guard2 := body.NewGuard(registry2, body.NewRuleEvaluator(body.AuthorityRule{Doll: "lifecycle-doll", Body: target2.ID(), Capability: "acceptance.echo", Operation: "echo", Decision: body.AuthorityAllow}))
	executor2 := dollmind.NewToolExecutor(registry2, guard2, policy, "lifecycle-doll", dollmind.WithPermissionRequests(requests2, "owner", time.Hour))
	if err = executor2.HandlePermissionResolution(ctx, approved); err != nil {
		return err
	}
	if err = executor2.HandlePermissionResolution(ctx, approved); err != nil {
		return err
	}
	target2.mu.Lock()
	count := len(target2.requests)
	target2.mu.Unlock()
	if count != 1 {
		return fmt.Errorf("Body execution count=%d", count)
	}
	stored, err := requests2.Get(ctx, id)
	if err != nil || stored.ContinuationStatus != permissionrequest.ContinuationSucceeded {
		return fmt.Errorf("completed continuation=%#v: %v", stored, err)
	}
	return nil
}

func runCommunicationRestart(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "neondoll-communication-restart-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "core.db")
	mapping := externalidentity.Mapping{Identity: externalidentity.Identity{GatewayInstanceID: "lifecycle-gateway", Platform: "acceptance", SubjectID: "owner"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}}
	resolver, _ := externalidentity.NewMemoryResolver([]externalidentity.Mapping{mapping})
	store, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	services, err := coreruntime.NewServices(ctx, store.(session.Store), coreruntime.WithExternalIdentityResolver(resolver))
	if err != nil {
		return err
	}
	router, _ := services.InteractionRouter(silentInteractionHandler{})
	gatewayRuntime, err := coreruntime.NewGatewayRuntime(ctx, services, store.(sessionevent.Store), store.(permissionrequest.Store), resolver, router, actionpolicy.Default())
	if err != nil {
		return err
	}
	adapter := &simulatedGateway{id: "lifecycle-gateway"}
	if err = gatewayRuntime.Register(ctx, adapter); err != nil {
		return err
	}
	incoming := events.NewDollMessage("route", "lifecycle-doll", "hello")
	if _, err = adapter.endpoint().HandlePlatformMessage(ctx, "acceptance/conversation/owner", gateway.VerifiedSender{Platform: "acceptance", SubjectID: "owner"}, &incoming); err != nil {
		return err
	}
	participants := services.Sessions().(session.ParticipantService)
	sender, err := communication.New(resolver, services.Sessions(), participants, gatewayRuntime.Events())
	if err != nil {
		return err
	}
	if err = gatewayRuntime.Unregister(ctx, "lifecycle-gateway"); err != nil {
		return err
	}
	registry := body.NewRegistry()
	executor := dollmind.NewToolExecutor(registry, body.NewGuard(registry, body.NewRuleEvaluator()), actionpolicy.Default(), "lifecycle-doll")
	executor.SetCommunicationSender(sender)
	provider := &scriptedProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "send", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner", "text": "durable message"}}}},
		{Content: "message remains queued for durable replay"},
	}}
	execution, err := cognitiveexecution.Execute(ctx, cognitiveexecution.Request{
		Provider: provider, ModelConfiguration: acceptanceModel(), Purpose: inference.PurposePlan,
		ToolExecutor: executor, CognitiveRunID: "run", IntentionID: "intention",
		BuildContext: func(_ context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
			messages := []inference.Message{{Role: "system", Content: "Queue one durable communication", Tools: state.Tools}}
			for _, exchange := range state.Exchanges {
				content, formatErr := cognitiveexecution.FormatToolExchange(exchange)
				if formatErr != nil {
					return nil, formatErr
				}
				call, result := exchange.Call, exchange.Result
				messages = append(messages, inference.Message{Role: "tool", Content: content, ToolCall: &call, ToolResult: &result})
			}
			return messages, nil
		},
	})
	if err != nil {
		return err
	}
	if len(execution.Exchanges) != 1 || execution.Exchanges[0].Result.ExecutionID == "" {
		return fmt.Errorf("queued communication exchange=%#v", execution.Exchanges)
	}
	deliveryID := execution.Exchanges[0].Result.ExecutionID
	provider.mu.Lock()
	inferenceCalls := len(provider.requests)
	provider.mu.Unlock()
	if err = gatewayRuntime.Shutdown(ctx); err != nil {
		return err
	}
	if err = store.Close(); err != nil {
		return err
	}
	reopened, err := persistence.NewStore(database)
	if err != nil {
		return err
	}
	defer reopened.Close()
	resolver2, _ := externalidentity.NewMemoryResolver([]externalidentity.Mapping{mapping})
	services2, err := coreruntime.NewServices(ctx, reopened.(session.Store), coreruntime.WithExternalIdentityResolver(resolver2))
	if err != nil {
		return err
	}
	router2, _ := services2.InteractionRouter(silentInteractionHandler{})
	runtime2, err := coreruntime.NewGatewayRuntime(ctx, services2, reopened.(sessionevent.Store), reopened.(permissionrequest.Store), resolver2, router2, actionpolicy.Default())
	if err != nil {
		return err
	}
	defer runtime2.Shutdown(context.Background())
	replacement := &simulatedGateway{id: "lifecycle-gateway"}
	if err = runtime2.Register(ctx, replacement); err != nil {
		return err
	}
	delivered := replacement.delivered()
	if len(delivered) != 1 {
		return fmt.Errorf("replayed deliveries=%d", len(delivered))
	}
	decoded, err := delivered[0].DecodeDelivery()
	if err != nil {
		return err
	}
	if decoded.ID != deliveryID || decoded.Event.Correlation.IntentionID != "intention" {
		return fmt.Errorf("replayed delivery=%#v", decoded)
	}
	provider.mu.Lock()
	afterReplay := len(provider.requests)
	provider.mu.Unlock()
	if afterReplay != inferenceCalls {
		return fmt.Errorf("inference calls after replay=%d, want unchanged %d", afterReplay, inferenceCalls)
	}
	return nil
}

func runDaemonShellBodyRestart(ctx context.Context, daemon *CoreDaemonDriver, shell *ShellBodyDriver) error {
	if err := shell.Prepare(ctx); err != nil {
		return err
	}
	instance, err := daemon.newInstance(ctx)
	if err != nil {
		return err
	}
	defer instance.close()
	if err = instance.start(ctx); err != nil {
		return err
	}
	before, err := instance.identity(ctx)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(instance.root, "shellbody")
	if _, err = shell.run(ctx, "--init", "--state-dir", stateDir, "--build", shell.Revision()); err != nil {
		return err
	}
	client := ownercontrol.NewClient(filepath.Join(instance.root, "data", "neondoll-admin.sock"))
	invite, err := client.CreateInvitation(ctx, time.Minute, []string{"process"}, dollnetwork.Endpoints{{URL: "http://" + instance.address}})
	if err != nil {
		return err
	}
	document := map[string]any{"version": 1, "invitation_id": invite.ID, "invitation_secret": invite.Secret, "expires_at": invite.ExpiresAt.UTC().Format(time.RFC3339), "bootstrap_endpoints": invite.BootstrapEndpoints}
	encoded, _ := json.Marshal(document)
	invitationPath := filepath.Join(instance.root, "invitation.json")
	if err = os.WriteFile(invitationPath, encoded, 0600); err != nil {
		return err
	}
	output, err := shell.run(ctx, "--enroll", invitationPath, "--core-url", "http://"+instance.address, "--state-dir", stateDir)
	if err != nil {
		return err
	}
	var bodyID string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "enrolled body_id=") {
			bodyID = strings.TrimPrefix(line, "enrolled body_id=")
		}
	}
	if bodyID == "" {
		return fmt.Errorf("Shell Body enrollment produced no ID: %s", tail(output, 2048))
	}
	bodyProcess, err := startManagedProcess(ctx, shell.binary, "--serve", "ws://"+instance.address+"/ws", "--state-dir", stateDir)
	if err != nil {
		return err
	}
	defer bodyProcess.Stop(3 * time.Second)
	if err = waitForExecution(ctx, instance.socket, bodyID, "before-restart"); err != nil {
		return err
	}
	if err = instance.stop(false); err != nil {
		return err
	}
	if err = instance.start(ctx); err != nil {
		return err
	}
	after, err := instance.identity(ctx)
	if err != nil || after != before {
		return fmt.Errorf("Core identity after restart=%q before=%q: %v", after, before, err)
	}
	if err = waitForExecution(ctx, instance.socket, bodyID, "after-restart"); err != nil {
		return err
	}
	return nil
}

func waitForExecution(ctx context.Context, socket, bodyID, id string) error {
	return waitFor(ctx, "Shell Body execution after Core restart", func() bool {
		result, err := executeLifecycle(ctx, socket, body.ExecutionRequest{ExecutionID: id, Doll: "core4-acceptance", Body: body.BodyID(bodyID), Capability: "process", Operation: "run", Arguments: map[string]any{"command": "printf", "args": []string{"lifecycle-acceptance"}}})
		return err == nil && result.Status == body.StatusSuccess && strings.Contains(result.Output, "lifecycle-acceptance")
	})
}
func executeLifecycle(ctx context.Context, socket string, value body.ExecutionRequest) (body.ExecutionResult, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	encoded, _ := json.Marshal(value)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/execute", bytes.NewReader(encoded))
	if err != nil {
		return body.ExecutionResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return body.ExecutionResult{}, err
	}
	defer response.Body.Close()
	var result body.ExecutionResult
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}
