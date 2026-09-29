package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/Core/WireGuard"
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

	// Validate the inference provider.
	if cfg.Inference.Provider != "openai" {
		log.Error("unsupported inference provider", map[string]any{"provider": cfg.Inference.Provider})
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

	// Create and start the WireGuard tunnel manager.
	wgManager := wireguard.NewManager(nil, ns, nil)
	if err := wgManager.Start(ctx, nw); err != nil {
		log.Error("wireguard manager start error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	log.Info("wireguard tunnel started", map[string]any{
		"overlay_addr": nw.Core.OverlayAddress.String(),
		"listen_port":  51820,
	})
	defer wgManager.Stop()

	// Create the inference provider.
	inferenceProvider := inference.NewOpenAIProvider(
		inference.WithBaseURL(cfg.Inference.BaseURL),
	)
	log.Info("inference provider created", map[string]any{
		"provider": cfg.Inference.Provider,
		"base_url": cfg.Inference.BaseURL,
	})

	// Create the Body Registry with the mandatory Local Body.
	bodyRegistry := body.NewRegistry()
	log.Info("body registry created", map[string]any{
		"local_id": string(body.LocalBodyID),
		"count":    bodyRegistry.Count(),
	})

	// Create the Interaction service (Doll Link ↔ Persistence bridge).
	interactionSvc := interaction.New(store, inferenceProvider, log)

	// Create WebSocket transport.
	wsCfg := ws.Config{Listen: listenAddr}
	wsServer := ws.New(wsCfg, log, interactionSvc)
	log.Info("ws transport created", map[string]any{"listen": listenAddr})

	// Create Pulse runner if enabled in config.
	// When Pulse is enabled, load (or create) a host doll state and wire
	// the real Doll Mind Scheduler as the MindEntrance, so spontaneous
	// Pulse opportunities can actually enter cognition.
	var pulseRunner *pulse.Runner
	if cfg.Core.Pulse.Enabled {
		// Determine which doll Pulse operates on behalf of.
		dollID := cfg.Core.Profile
		if dollID == "" {
			dollID = "default"
		}

		// Load the host doll from persistence. If it does not exist,
		// create a minimal initial state and persist it. Any other load
		// failure (corruption, decoding, I/O) is fatal — never silently
		// replace an existing Doll.
		dollState, err := store.LoadDoll(ctx, dollID)
		switch {
		case err == nil:
			// existing doll loaded successfully
			log.Info("loaded existing doll state", map[string]any{"doll_id": dollID})

		case errors.Is(err, persistence.ErrDollNotFound):
			log.Info("no existing doll found, creating initial state",
				map[string]any{"doll_id": dollID})
			ds := dollstate.NewDollState()
			ds.Identity = dollstate.Identity{
				DollID:        dollID,
				CanonicalName: dollID,
			}
			if err := store.SaveDoll(ctx, &ds); err != nil {
				log.Error("save initial doll state error", map[string]any{"error": err.Error()})
				os.Exit(1)
			}
			dollState = &ds
			log.Info("initial doll state created", map[string]any{"doll_id": dollID})

		default:
			log.Error("fatal: failed to load existing doll — refusing to create replacement", map[string]any{
				"doll_id": dollID,
				"error":   err.Error(),
			})
			os.Exit(1)
		}

		// Create the MindAPI and Scheduler with the real provider
		// and persist-backed API.
		mindAPI := &productionMindAPI{state: dollState, store: store, provider: inferenceProvider}

		// Create the Scheduler with the real provider and persist-backed API.
		scheduler := dollmind.New(inferenceProvider, log, mindAPI)

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
	if err := wsServer.Start(ctx); err != nil {
		log.Error("ws server error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}

	log.Info("neondoll stopped")
}
