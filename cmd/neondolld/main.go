package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Neon-Dolls/neondoll/Core/API"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Logger"
)

func main() {
	log := logger.New(logger.InfoLevel, os.Stdout)
	log.Info("NeonDoll starting", map[string]any{
		"version": "0.1.0-draft",
		"core":    "core1",
	})

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		log.Warn("no config file found, using defaults", map[string]any{"error": err.Error()})
		d := config.Defaults()
		cfg = &d
	}

	log.Info("configuration loaded", map[string]any{
		"profile": cfg.Core.Profile,
		"env":     cfg.Core.Environment,
	})

	// Start API server
	apiSrv := api.New(api.Config{
		Listen:     cfg.HTTP.Listen,
		EnableCORS: cfg.HTTP.EnableCORS,
	}, log)

	go func() {
		log.Info("starting api server", map[string]any{"addr": cfg.HTTP.Listen})
		if err := apiSrv.Start(); err != nil {
			log.Error("api server error", map[string]any{"error": err.Error()})
		}
	}()

	// Wait for shutdown signal
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10)
	defer cancel()

	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", map[string]any{"error": err.Error()})
	}
	log.Info("NeonDoll stopped")
}

func loadConfig() (*config.Config, error) {
	path, err := config.FindConfig()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return config.Load(path)
}