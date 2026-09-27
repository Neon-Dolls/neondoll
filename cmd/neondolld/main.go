package main

import (
	"context"
	"encoding/json"
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
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
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

	// Create the base context for all services.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		// create a minimal initial state and persist it.
		dollState, err := store.LoadDoll(ctx, dollID)
		if err != nil {
			log.Info("creating initial doll state",
				map[string]any{"doll_id": dollID, "reason": err.Error()})
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
		}

		// Create the MindAPI and Scheduler with the real provider
		// and persist-backed API.
		mindAPI := &productionMindAPI{state: dollState, store: store, provider: inferenceProvider}

		// Create the Scheduler with the real provider and persist-backed API.
		scheduler := dollmind.New(inferenceProvider, log, mindAPI)

		// Wire the Scheduler as Pulse's MindEntrance.
		pulseRunner = pulse.NewRunner(cfg.Core.Pulse, pulse.NewRealClock(), pulse.NewProductionRNG(), log, scheduler)
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
