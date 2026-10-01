package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
	"github.com/Neon-Dolls/neondoll/pkg/version"
)

func main() {
	// Define flags for the neondoll-body command.
	var (
		connectCmd   = flag.NewFlagSet("connect", flag.ExitOnError)
		connectFlags = struct {
			dataDir string
			timeout time.Duration
			verbose bool
		}{}
	)

	connectCmd.StringVar(&connectFlags.dataDir, "datadir", "", "data directory for the Body")
	connectCmd.DurationVar(&connectFlags.timeout, "timeout", 10*time.Second, "timeout for connection attempt")
	connectCmd.BoolVar(&connectFlags.verbose, "v", false, "verbose logging")

	// Parse command line arguments.
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: neondoll-body <command> [args]\n")
		fmt.Fprintf(os.Stderr, "commands:\n")
		fmt.Fprintf(os.Stderr, "  connect   Verify connectivity to a Core Doll Network peer\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "connect":
		connectCmd.Parse(os.Args[2:])
		if err := runConnect(connectFlags); err != nil {
			log.Fatalf("connect: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runConnect(flags struct {
	dataDir string
	timeout time.Duration
	verbose bool
}) error {
	// Set up logger.
	log := logger.New(logger.InfoLevel, os.Stdout)
	if flags.verbose {
		log = logger.New(logger.DebugLevel, os.Stdout)
	}

	// Determine data directory.
	if flags.dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		flags.dataDir = filepath.Join(home, ".neondoll", "body")
	}

	// Load config (optional — use defaults if not found).
	cfg, err := Config.Load(filepath.Join(flags.dataDir, "config.json"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config load error: %w", err)
	}
	if cfg == nil {
		cfg = &Config.Defaults()
	}

	// Ensure data directory exists.
	if err := os.MkdirAll(flags.dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir error: %w", err)
	}

	// Open persistence store.
	dbPath := filepath.Join(flags.dataDir, "neondoll.db")
	store, err := Persistence.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("persistence open error: %w", err)
	}
	defer store.Close()

	// Create the Body registry.
	bodyRegistry := Body.NewRegistry()

	// Load the local Body's membership state.
	membershipStatePath := filepath.Join(flags.dataDir, "membership.json")
	membershipData, err := os.ReadFile(membershipStatePath)
	if err != nil {
		return fmt.Errorf("failed to read membership state: %w", err)
	}
	var membershipState Body.MembershipState
	if err := json.Unmarshal(membershipData, &membershipState); err != nil {
		return fmt.Errorf("failed to parse membership state: %w", err)
	}

	// Select a Core IPv6 from the membership state.
	coreIP, err := membershipState.SelectCoreIPv6()
	if err != nil {
		return fmt.Errorf("failed to select Core IPv6: %w", err)
	}

	// Load or create the Body's WireGuard tunnel.
	tun, err := loadOrCreateTunnel(flags.dataDir)
	if err != nil {
		return fmt.Errorf("failed to load/create tunnel: %w", err)
	}
	defer tun.Close()

	// Use the tunnel's Ping6Core method to send an ICMPv6 echo request to coreIP.
	err = tun.Ping6Core(coreIP, flags.timeout)
	if err != nil {
		return fmt.Errorf("connectivity verification failed: %w", err)
	}

	// Only print success after verification passes.
	log.Info("connectivity verification successful", map[string]any{
		"core_ip": coreIP.String(),
		"timeout": flags.timeout,
	})
	fmt.Println("tunnel established successfully")
	return nil
}
