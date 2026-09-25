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
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
	"github.com/Neon-Dolls/neondoll/pkg/version"
)

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
	var pulseRunner *pulse.Runner
	if cfg.Core.Pulse.Enabled {
		pulseRunner = pulse.NewRunner(cfg.Core.Pulse, pulse.NewRealClock(), log)
		if err := pulseRunner.Start(ctx); err != nil {
			log.Error("pulse runner start error", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		log.Info("pulse runner started", map[string]any{
			"min_wake_spacing_seconds": cfg.Core.Pulse.MinWakeSpacing,
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
