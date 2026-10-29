package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Extension"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
	"github.com/Neon-Dolls/neondoll/pkg/version"
)

// productionMindAPI wraps the persistence store, inference provider, and a
// loaded doll state as a dollmind.MindAPI for the production daemon.
type productionMindAPI struct {
	state    *dollstate.DollState
	store    persistence.Store
	provider inference.Provider
}

func (m *productionMindAPI) Inference() inference.Provider { return m.provider }
func (m *productionMindAPI) State() *dollstate.DollState   { return m.state }
func (m *productionMindAPI) Save() error                   { return m.store.SaveDoll(context.Background(), m.state) }

// wireguardListenPort is the stable UDP port the Core's WireGuard tunnel
// binds. Keeping it stable across reconstructions is what lets a paired Body
// reconnect to the reconstructed Core without a new endpoint advertisement.
const wireguardListenPort = 51820

func resolveTaskProvider(resolver *config.ModelResolver, task config.InferenceTask) (config.ModelConfiguration, inference.Provider, error) {
	model, err := resolver.ResolveTask(task)
	if err != nil {
		return config.ModelConfiguration{}, nil, err
	}
	provider, err := inference.NewProvider(model.Provider, model.Endpoint)
	if err != nil {
		return config.ModelConfiguration{}, nil, fmt.Errorf("task %q model %q: %w", task, model.Name, err)
	}
	return model, provider, nil
}

func newProductionToolExecutor(registry *body.Registry, dollID string, actionRules []actionpolicy.Rule, configuredRules []config.BodyAuthorizationRule, configuredPreferences map[string]int, options ...dollmind.ToolExecutorOption) (*dollmind.ToolExecutor, error) {
	if dollID == "" {
		return nil, fmt.Errorf("production tool executor: Doll identity unavailable")
	}
	if _, ok := registry.Local(); !ok {
		return nil, fmt.Errorf("production tool executor: local Body unavailable")
	}
	actionPolicy, err := actionpolicy.New(actionRules)
	if err != nil {
		return nil, fmt.Errorf("production tool executor: action policy: %w", err)
	}
	bodyRules, err := productionBodyAuthorityRules(dollID, configuredRules)
	if err != nil {
		return nil, err
	}
	bodyAuthority := body.NewRuleEvaluator(bodyRules...)
	preferences := make(map[body.BodyID]int, len(configuredPreferences))
	for bodyID, preference := range configuredPreferences {
		preferences[body.BodyID(bodyID)] = preference
	}
	options = append(options, dollmind.WithBodyPreferences(preferences))
	return dollmind.NewToolExecutor(registry, body.NewGuard(registry, bodyAuthority), actionPolicy, dollID, options...), nil
}

func productionBodyAuthorityRules(dollID string, configured []config.BodyAuthorizationRule) ([]body.AuthorityRule, error) {
	if len(configured) == 0 {
		return []body.AuthorityRule{
			{Doll: dollID, Body: body.LocalBodyID, Capability: "runtime.info", Operation: "read", Decision: body.AuthorityAllow},
			{Doll: dollID, Body: body.LocalBodyID, Capability: "runtime.info", Operation: "list", Decision: body.AuthorityAllow},
		}, nil
	}
	rules := make([]body.AuthorityRule, len(configured))
	for index, rule := range configured {
		if rule.Body == "" || rule.Capability == "" || rule.Operation == "" ||
			(rule.Decision != body.AuthorityAllow && rule.Decision != body.AuthorityDeny) ||
			(rule.Scope != "" && rule.Scope != body.AuthorityScopeBody && rule.Scope != body.AuthorityScopeGlobal) {
			return nil, fmt.Errorf("production tool executor: invalid Body authorization rule %d", index)
		}
		ruleDoll := rule.Doll
		if ruleDoll == "" {
			ruleDoll = dollID
		}
		rules[index] = body.AuthorityRule{
			Doll: ruleDoll, Body: body.BodyID(rule.Body), Capability: rule.Capability, Operation: rule.Operation,
			Decision: rule.Decision, Reason: rule.Reason, Message: rule.Message, Scope: rule.Scope,
		}
	}
	return rules, nil
}

func loadProductionDoll(ctx context.Context, store persistence.Store, dollID string) (*dollstate.DollState, error) {
	state, err := store.LoadDoll(ctx, dollID)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, persistence.ErrDollNotFound) {
		return nil, err
	}
	created := dollstate.NewDollState()
	created.Identity = dollstate.Identity{DollID: dollID, CanonicalName: dollID}
	if err := store.SaveDoll(ctx, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func main() {
	log := logger.New(logger.InfoLevel, os.Stdout)
	log.Info(fmt.Sprintf("neondoll v%s starting — Core 1", version.String()))

	// Load config (optional — use defaults if not found).
	cfg, err := config.Load("config.json")
	if err != nil && !os.IsNotExist(err) {
		log.Error("config load error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	// If config didn't load, use defaults.
	if cfg == nil {
		defaults := config.Defaults()
		cfg = &defaults
	}
	externalIdentityResolver, err := config.NewExternalIdentityResolver(cfg.Core)
	if err != nil {
		log.Error("external identity resolver setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	modelResolver, err := config.NewModelResolver(cfg.Inference)
	if err != nil {
		log.Error("invalid model configuration", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	interactionModel, interactionProvider, err := resolveTaskProvider(modelResolver, config.InferenceTaskInteraction)
	if err != nil {
		log.Error("Interaction model setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	orientModel, orientProvider, err := resolveTaskProvider(modelResolver, config.InferenceTaskOrient)
	if err != nil {
		log.Error("Orient model setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	planModel, planProvider, err := resolveTaskProvider(modelResolver, config.InferenceTaskPlan)
	if err != nil {
		log.Error("Plan model setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	directModel, directProvider, err := resolveTaskProvider(modelResolver, config.InferenceTaskDirectCognition)
	if err != nil {
		log.Error("direct cognition model setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	// Ensure database directory exists.
	if err := os.MkdirAll(cfg.Paths.DataDir, 0o755); err != nil {
		log.Error("mkdir error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	// Determine listen address.
	listenAddr := cfg.Link.WebSocket.Listen
	if cfg.HTTP.Enabled {
		listenAddr = cfg.HTTP.Listen
	}

	// Open persistence store.
	dbPath := filepath.Join(cfg.Paths.DataDir, "neondoll.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		log.Error("persistence open error", map[string]any{"error": err.Error(), "path": dbPath})
		os.Exit(1)
	}
	defer store.Close()
	log.Info("persistence opened", map[string]any{"path": dbPath})

	// Create the base context for all services.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Construct process-local Core services over the daemon's existing durable
	// store. The session service owns no transport or model context and remains
	// available to future internal Gateway hosts through this runtime boundary.
	sessionStore, ok := store.(session.Store)
	if !ok {
		log.Error("store does not implement Interaction Session Store")
		os.Exit(1)
	}
	coreServices, err := runtime.NewServices(ctx, sessionStore, runtime.WithExternalIdentityResolver(externalIdentityResolver))
	if err != nil {
		log.Error("Core runtime services setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	trustedParticipants, ok := coreServices.Sessions().(session.TrustedParticipantService)
	if !ok {
		log.Error("external identity reconciliation error", map[string]any{"error": "trusted participant service unavailable"})
		os.Exit(1)
	}
	configuredSources := make([]session.ParticipantSource, 0, len(cfg.Core.ExternalIdentities))
	configuredPrincipals := make([]string, 0, len(cfg.Core.ExternalIdentities))
	seenPrincipals := make(map[string]struct{})
	for _, mapping := range cfg.Core.ExternalIdentities {
		configuredSources = append(configuredSources, session.ParticipantSource{GatewayInstanceID: string(mapping.Identity.GatewayInstanceID), Platform: mapping.Identity.Platform, SubjectID: mapping.Identity.SubjectID, PrincipalID: string(mapping.Principal.ID)})
		if _, exists := seenPrincipals[string(mapping.Principal.ID)]; !exists {
			configuredPrincipals = append(configuredPrincipals, string(mapping.Principal.ID))
			seenPrincipals[string(mapping.Principal.ID)] = struct{}{}
		}
	}
	if err = trustedParticipants.ReconcileParticipantSources(ctx, configuredSources, configuredPrincipals); err != nil {
		log.Error("external identity reconciliation error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	activeSessions, err := coreServices.Sessions().ListActive(ctx, nil)
	if err != nil {
		log.Error("Interaction Session reconstruction error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	log.Info("Interaction Session service ready", map[string]any{"active_sessions": len(activeSessions)})

	// Load or create the Doll Network state for WireGuard overlay.
	ns, ok := store.(network.NetworkStore)
	if !ok {
		log.Error("store does not implement NetworkStore")
		os.Exit(1)
	}
	nw, err := ns.LoadNetwork(ctx)
	if err != nil {
		log.Error("load network error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	if nw == nil {
		nw, err = network.NewNetwork(network.GenerateNetworkID())
		if err != nil {
			log.Error("create network error", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		if err := ns.SaveNetwork(ctx, nw); err != nil {
			log.Error("save new network error", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		log.Info("new network created", map[string]any{"network_id": string(nw.NetworkID)})
	}
	log.Info("network state loaded", map[string]any{"network_id": string(nw.NetworkID)})

	// Reconstruct the network runtime from durable state (M7.3). This loads
	// the persisted Core identity, WireGuard keys, overlay address, and
	// authorized Body memberships — never generating a replacement identity
	// while valid state exists.
	rt, err := runtime.Reconstruct(ctx, nw, ns, nil, runtime.WithListenPort(wireguardListenPort))
	if err != nil {
		log.Error("network runtime reconstruct error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	log.Info("network runtime reconstructed", map[string]any{
		"overlay_addr": nw.Core.OverlayAddress.String(),
		"listen_port":  wireguardListenPort,
	})
	defer rt.Stop()

	log.Info("task inference models resolved", map[string]any{
		"interaction": interactionModel.Name,
		"orient":      orientModel.Name,
		"plan":        planModel.Name,
		"direct":      directModel.Name,
	})

	// Create the Body Registry with the mandatory Local Body.
	bodyRegistry := body.NewRegistry()
	log.Info("body registry created", map[string]any{
		"local_id": string(body.LocalBodyID),
		"count":    bodyRegistry.Count(),
	})
	dollID := cfg.Core.Profile
	if dollID == "" {
		dollID = "default"
	}
	dollState, err := loadProductionDoll(ctx, store, dollID)
	if err != nil {
		log.Error("production Doll state setup error", map[string]any{"doll_id": dollID, "error": err.Error()})
		os.Exit(1)
	}
	actionPolicy, err := actionpolicy.New(cfg.Core.ActionPolicy)
	if err != nil {
		log.Error("Gateway permission policy setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	permissionStore, ok := store.(permissionrequest.Store)
	if !ok {
		log.Error("store does not implement Permission Request Store")
		os.Exit(1)
	}
	permissionManager := permissionrequest.New(permissionStore, actionPolicy)
	for _, rule := range cfg.Core.ActionPolicy {
		if rule.Decision == actionpolicy.DecisionRequireApproval && strings.TrimSpace(dollState.Owner.OwnerID) == "" {
			log.Error("approval-required action policy has no configured Doll Owner", map[string]any{"doll_id": dollState.Identity.DollID})
			os.Exit(1)
		}
	}
	cognitionToolExecutor, err := newProductionToolExecutor(bodyRegistry, dollState.Identity.DollID, cfg.Core.ActionPolicy, cfg.Core.BodyAuthorization, cfg.Core.BodyPreferences,
		dollmind.WithPermissionRequests(permissionManager, dollState.Owner.OwnerID, 15*time.Minute))
	if err != nil {
		log.Error("cognition tool executor setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	acceptanceControl, err := startAcceptanceControl(bodyRegistry)
	if err != nil {
		log.Error("acceptance control setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	defer acceptanceControl()
	directCognitionProvider := inference.NewToolEnabledProvider(directProvider)
	orientCognitionProvider := inference.NewToolEnabledProvider(orientProvider)
	mindAPI := &productionMindAPI{state: dollState, store: store, provider: directCognitionProvider}
	scheduler := dollmind.New(directCognitionProvider, log, mindAPI,
		dollmind.WithModelConfiguration(directModel),
		dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, orientCognitionProvider, orientModel),
		dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, inference.NewToolEnabledProvider(planProvider), planModel),
		dollmind.WithToolExecutor(cognitionToolExecutor),
	)
	cognitionToolExecutor.SetPermissionResolutionNotifier(scheduler)

	// Create the Interaction service (Doll Link ↔ Persistence bridge).
	interactionSvc := interaction.New(store, inference.NewToolEnabledProvider(interactionProvider), log,
		interaction.WithModelConfiguration(interactionModel),
		interaction.WithToolExecutor(cognitionToolExecutor),
	)
	interactionRouter, err := coreServices.InteractionRouter(interactionSvc)
	if err != nil {
		log.Error("Interaction Session router setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	bodyIngress, err := interaction.NewBodyIngress(coreServices.Sessions(), interactionRouter)
	if err != nil {
		log.Error("authenticated Body interaction ingress setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	eventStore, ok := store.(sessionevent.Store)
	if !ok {
		log.Error("store does not implement Session Event Store")
		os.Exit(1)
	}
	gatewayRuntime, err := runtime.NewGatewayRuntime(ctx, coreServices, eventStore, permissionStore,
		externalIdentityResolver, interactionRouter, actionPolicy,
		runtime.WithPermissionManager(permissionManager), runtime.WithPermissionResolutionHandler(cognitionToolExecutor))
	if err != nil {
		log.Error("Gateway runtime setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	defer gatewayRuntime.Shutdown(context.Background())
	participants, ok := coreServices.Sessions().(session.ParticipantService)
	if !ok {
		log.Error("communication setup error", map[string]any{"error": "Session participant service unavailable"})
		os.Exit(1)
	}
	communicationService, err := communication.New(coreServices.Sessions(), participants, gatewayRuntime.Events())
	if err != nil {
		log.Error("communication setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	cognitionToolExecutor.SetCommunicationSender(communicationService)
	extensionState := extension.NewFileStateStore(filepath.Join(cfg.Paths.DataDir, "extensions", "state"))
	extensionRuntime, err := extension.NewWASMRuntime(ctx, extensionState, cfg.Paths.SecretsDir)
	if err != nil {
		log.Error("extension runtime setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	manifestJSON, err := json.Marshal(cfg.Extensions)
	var extensionManifests []extension.Manifest
	if err == nil {
		err = json.Unmarshal(manifestJSON, &extensionManifests)
	}
	if err != nil {
		log.Error("extension manifest setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	extensionManager, err := extension.NewManager(extensionRuntime, gatewayRuntime, cfg.Paths.DataDir, extensionManifests)
	if err == nil {
		err = extensionManager.LoadEnabled(ctx)
	}
	if err != nil {
		log.Error("extension manager startup error", map[string]any{"error": fmt.Sprint(err)})
		os.Exit(1)
	}
	defer extensionManager.Shutdown(context.Background())
	log.Info("Gateway runtime ready")

	// Create WebSocket transport.
	wsCfg := ws.Config{Listen: listenAddr}
	wsServer := ws.New(wsCfg, log, bodyIngress)
	invitationStore, ok := store.(invitation.Store)
	if !ok {
		log.Error("store does not implement Invitation Store")
		os.Exit(1)
	}
	enrollmentStore, ok := store.(enrollment.Store)
	if !ok {
		log.Error("store does not implement Enrollment Store")
		os.Exit(1)
	}
	invitationService := invitation.NewService(invitationStore, invitation.RealClock{})
	ownerBoundary := ownercontrol.NewBoundary()
	enrollmentService, err := enrollment.NewService(ctx, enrollmentStore, nw, invitationService, ownerBoundary, nil, enrollment.DefaultConfig())
	if err != nil {
		log.Error("Body enrollment service setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	enrollment.RegisterBodyEndpoints(wsServer, enrollmentService)
	wsServer.SetBodyAuthenticator(enrollmentService)
	log.Info("Body enrollment endpoints registered", map[string]any{"core_id": enrollmentService.CoreIdentity().CoreID})
	remoteRegistrar, err := body.NewRemoteRegistrar(bodyRegistry, ns, wsServer)
	if err != nil {
		log.Error("remote Body registrar setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	wsServer.SetExecutionSessionLifecycle(remoteRegistrar)
	enrollmentService.SetRevocationLifecycle(wsServer, rt)
	log.Info("ws transport created", map[string]any{"listen": listenAddr})

	adminSocket := filepath.Join(cfg.Paths.DataDir, "neondoll-admin.sock")
	ownerService := ownercontrol.NewService(enrollmentService, invitationService, ownerBoundary)
	ownerServer, err := ownercontrol.NewServer(adminSocket, ownerService)
	if err != nil {
		log.Error("owner control setup error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	go func() {
		if err := ownerServer.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("owner control server error", map[string]any{"error": err.Error()})
			cancel()
		}
	}()
	log.Info("owner control ready", map[string]any{"socket": adminSocket})

	// HTTP-enabled deployments expose the configured host listener for initial
	// enrollment as well as authenticated Doll Link traffic. An unenrolled Body
	// cannot reach an overlay-only endpoint. Overlay-only deployments retain the
	// existing netstack listener when HTTP exposure is explicitly disabled.
	if tnet := rt.Tunnel().Netstack(); !cfg.HTTP.Enabled && tnet != nil {
		// Parse port from the configured listen address.
		_, portStr, err := net.SplitHostPort(listenAddr)
		port := 8080
		if err == nil {
			if p, parseErr := strconv.Atoi(portStr); parseErr == nil {
				port = p
			}
		}
		// Bind to all addresses on the netstack — incoming WG-decrypted
		// traffic from overlay peers will arrive here.
		listener, listenErr := tnet.ListenTCP(&net.TCPAddr{
			IP:   net.IPv6unspecified,
			Port: port,
		})
		if listenErr != nil {
			log.Error("netstack ws listen error", map[string]any{"error": listenErr.Error()})
			os.Exit(1)
		}
		wsServer.SetListener(listener)
		log.Info("ws server listening through wireguard overlay",
			map[string]any{"port": port, "overlay_addr": nw.Core.OverlayAddress.String()})
	}

	// Create Pulse runner if enabled in config.
	// When Pulse is enabled, load (or create) a host doll state and wire
	// the real Doll Mind Scheduler as the MindEntrance, so spontaneous
	// Pulse opportunities can actually enter cognition.
	var pulseRunner *pulse.Runner
	if cfg.Core.Pulse.Enabled {
		// Wire the Scheduler as Pulse's MindEntrance.
		pulseRunner = pulse.NewRunner(cfg.Core.Pulse, pulse.NewRealClock(), pulse.NewProductionRNG(), log, scheduler)

		// ── Register semantic subjects from the doll's identity ──────
		// Pulse observes changes to core identity and soul content to
		// detect when a doll has been "neglected" or has "changed"
		// since the last presentation. These are the canonical subjects;
		// additional subjects may be registered later by Core.
		subjects := []pulse.PulseSubjectState{
			{SubjectID: "persona", ChangesSincePresent: 0},
			{SubjectID: "soul", ChangesSincePresent: 0},
		}
		pulseRunner.UpdateSubjects(subjects)

		cpStore := store.(persistence.CheckpointStore)

		// Load the checkpoint BEFORE creating the runner so we can
		// decide first-run vs corrupt before constructing anything.
		cpData, cpLoadErr := cpStore.LoadPulseCheckpoint(ctx, dollID)
		switch {
		case cpLoadErr == nil:
			// Checkpoint loaded — deserialize and restore.
			cp, cpErr := pulse.UnmarshalCheckpoint(cpData)
			if cpErr != nil {
				log.Error("fatal: pulse checkpoint corrupt — refusing to start", map[string]any{
					"doll_id": dollID,
					"error":   cpErr.Error(),
				})
				os.Exit(1)
			}
			pulseRunner.RestoreFromCheckpoint(cp)
			log.Info("pulse checkpoint restored", map[string]any{"doll_id": dollID})

		case errors.Is(cpLoadErr, persistence.ErrPulseCheckpointNotFound):
			// Normal first-run — no checkpoint to restore.
			log.Info("no pulse checkpoint found, starting fresh", map[string]any{"doll_id": dollID})

		default:
			log.Error("fatal: failed to load pulse checkpoint", map[string]any{
				"doll_id": dollID,
				"error":   cpLoadErr.Error(),
			})
			os.Exit(1)
		}

		// ── Wire durable checkpoint writer ───────────────────────────
		// After every wake admission and cognition settlement, the Runner
		// invokes this writer to persist bookkeeping. Errors are surfaced
		// by the Runner: admission checkpoint failures are logged,
		// settlement checkpoint failures are returned from the lifecycle.
		pulseRunner.CheckpointWriter = func(cp pulse.PulseCheckpoint) error {
			cp.DollID = dollID
			data, err := pulse.MarshalCheckpoint(cp)
			if err != nil {
				return fmt.Errorf("marshal checkpoint: %w", err)
			}
			return cpStore.SavePulseCheckpoint(ctx, dollID, data)
		}

		if err := pulseRunner.Start(ctx); err != nil {
			log.Error("pulse runner start error", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		log.Info("pulse runner started", map[string]any{
			"enabled": cfg.Core.Pulse.Enabled,
			"doll_id": dollID,
		})
	}

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Info("signal received", map[string]any{"signal": sig.String()})

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := wsServer.Shutdown(shutdownCtx); err != nil {
			log.Error("ws shutdown error", map[string]any{"error": err.Error()})
		}
		if err := ownerServer.Shutdown(shutdownCtx); err != nil {
			log.Error("owner control shutdown error", map[string]any{"error": err.Error()})
		}
		if err := gatewayRuntime.Shutdown(shutdownCtx); err != nil {
			log.Error("Gateway runtime shutdown error", map[string]any{"error": err.Error()})
		}
		if pulseRunner != nil {
			pulseRunner.Stop()
			log.Info("pulse runner stopped")
		}
		cancel()
	}()

	// Write a small startup banner.
	info := map[string]any{
		"http_listen": listenAddr,
		"db_path":     dbPath,
		"version":     1,
	}
	b, _ := json.Marshal(info)
	fmt.Fprintf(os.Stderr, "neondoll started: %s\n", string(b))

	// Start the WS server (blocking).
	if err := wsServer.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("ws server error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	log.Info("neondoll stopped")
}
