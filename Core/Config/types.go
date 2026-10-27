package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// secAsDuration converts an int64 seconds value (as stored in PulseConfig)
// to a time.Duration, returning 0 for zero (unset) inputs.
func secAsDuration(s int64) time.Duration {
	return time.Duration(s) * time.Second
}

// RelayConfig for the Doll Relay connection.
type RelayConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Token   string `json:"token"`
}

// Config holds the Doll's runtime configuration.
type Config struct {
	Core       CoreConfig          `json:"core"`
	Console    ConsoleConfig       `json:"console"`
	Inference  InferenceConfig     `json:"inference"`
	Paths      PathConfig          `json:"paths"`
	Link       LinkConfig          `json:"link"`
	HTTP       HTTPConfig          `json:"http"`
	Relay      RelayConfig         `json:"relay"`
	Extensions []ExtensionManifest `json:"extensions,omitempty"`
}

type ExtensionArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type ExtensionGateway struct {
	Platform           string `json:"platform"`
	DollID             string `json:"doll_id"`
	ConversationPrefix string `json:"conversation_prefix"`
}
type ExtensionManifest struct {
	Version       int               `json:"version"`
	ExtensionID   string            `json:"extension_id"`
	InstanceID    string            `json:"instance_id"`
	Contract      string            `json:"contract"`
	ABI           int               `json:"abi_version"`
	Enabled       bool              `json:"enabled"`
	Artifact      ExtensionArtifact `json:"artifact"`
	Configuration json.RawMessage   `json:"configuration,omitempty"`
	Requested     []string          `json:"requested_capabilities,omitempty"`
	Granted       []string          `json:"granted_capabilities,omitempty"`
	HTTPAllowlist []string          `json:"http_allowlist,omitempty"`
	Secrets       map[string]string `json:"secrets,omitempty"`
	Gateway       *ExtensionGateway `json:"gateway,omitempty"`
}

// PulseConfig holds runtime configuration for the Pulse temporal subsystem.
// Fields are the canonical M1+ contract; some only gain semantics in later
// milestones. All duration values are in seconds (int64); internally converted
// to time.Duration via secAsDuration(). Zero means "not configured" and the
// field (or its implied behaviour) is disabled unless documented otherwise.
type PulseConfig struct {
	Enabled        bool  `json:"enabled"`
	IdleHorizon    int64 `json:"idle_horizon"`             // seconds; M2+: neglect idle without cognition
	NeglectHorizon int64 `json:"neglect_horizon"`          // seconds; M2+: neglect threshold since last cognition
	ChangeHorizon  int64 `json:"change_horizon"`           // count of state-change occurrences before triggering
	WakeCooldown   int64 `json:"wake_cooldown"`            // seconds; later: cooldown after wake
	MinWakeSpacing int64 `json:"min_wake_spacing_seconds"` // seconds; M3+: hard guard between spontaneous wakes. 0 = guard disabled.
}

// CoreConfig for runtime settings.
type CoreConfig struct {
	Profile            string                     `json:"profile"`
	Environment        string                     `json:"environment"`
	Pulse              PulseConfig                `json:"pulse"`
	BodyPreferences    map[string]int             `json:"body_preferences,omitempty"`
	BodyAuthorization  []BodyAuthorizationRule    `json:"body_authorization,omitempty"`
	ActionPolicy       []actionpolicy.Rule        `json:"action_policy,omitempty"`
	ExternalIdentities []externalidentity.Mapping `json:"external_identity_mappings,omitempty"`
}

// BodyAuthorizationRule configures the existing per-Body Guard evaluator.
// An empty Doll binds the rule to the production Core's persisted Doll ID.
type BodyAuthorizationRule struct {
	Doll       string                 `json:"doll,omitempty"`
	Body       string                 `json:"body"`
	Capability string                 `json:"capability"`
	Operation  string                 `json:"operation"`
	Decision   body.AuthorityDecision `json:"decision"`
	Reason     string                 `json:"reason,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Scope      body.AuthorityScope    `json:"scope,omitempty"`
}

// ConsoleConfig for REPL and terminal interaction.
type ConsoleConfig struct {
	Enabled bool   `json:"enabled"`
	Prompt  string `json:"prompt"`
}

// PathConfig for local storage paths.
type PathConfig struct {
	StateDir   string `json:"state_dir"`
	SecretsDir string `json:"secrets_dir"`
	DataDir    string `json:"data_dir"`
}

// InferenceConfig selects from statically configured inference models.
type InferenceConfig struct {
	DefaultModel string                        `json:"default_model"`
	Models       map[string]ModelConfiguration `json:"models"`
	Tasks        map[InferenceTask]string      `json:"tasks,omitempty"`
}

// LinkConfig for transport connectivity.
type LinkConfig struct {
	WebSocket WebSocketConfig `json:"websocket"`
}

// WebSocketConfig for the WS server.
type WebSocketConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

// HTTPConfig for the API server.
type HTTPConfig struct {
	Enabled    bool   `json:"enabled"`
	Listen     string `json:"listen"`
	EnableCORS bool   `json:"enable_cors"`
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	budget := inference.DefaultModelContextBudget()
	return Config{
		Core: CoreConfig{
			Profile:     "default",
			Environment: "development",
		},
		Console: ConsoleConfig{
			Enabled: true,
			Prompt:  "> ",
		},
		Inference: InferenceConfig{
			DefaultModel: "default",
			Models: map[string]ModelConfiguration{
				"default": {
					Provider: "openai",
					Model:    "default",
					Endpoint: "http://localhost:8080",
					Budget:   budget,
				},
			},
		},
		Paths: PathConfig{
			StateDir:   "data/state",
			SecretsDir: "data/secrets",
			DataDir:    "data",
		},
		Link: LinkConfig{
			WebSocket: WebSocketConfig{
				Enabled: false,
				Listen:  "127.0.0.1:8765",
			},
		},
		HTTP: HTTPConfig{
			Enabled:    true,
			Listen:     "127.0.0.1:8080",
			EnableCORS: true,
		},
		Relay: RelayConfig{
			Enabled: false,
		},
	}
}

// Load reads and parses a JSON config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config load: %w", err)
	}
	cfg := Defaults()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	if _, err := NewModelResolver(cfg.Inference); err != nil {
		return nil, fmt.Errorf("config inference: %w", err)
	}
	if _, err := actionpolicy.New(cfg.Core.ActionPolicy); err != nil {
		return nil, fmt.Errorf("config action policy: %w", err)
	}
	if _, err := NewExternalIdentityResolver(cfg.Core); err != nil {
		return nil, fmt.Errorf("config external identities: %w", err)
	}
	instances := map[string]bool{}
	for _, manifest := range cfg.Extensions {
		if manifest.Version != 1 || manifest.Contract != "gateway" || manifest.ABI != 2 || manifest.ExtensionID == "" || manifest.InstanceID == "" || manifest.Artifact.Path == "" || len(manifest.Artifact.SHA256) != 64 || manifest.Gateway == nil {
			return nil, fmt.Errorf("config extensions: invalid manifest %q", manifest.InstanceID)
		}
		if instances[manifest.InstanceID] {
			return nil, fmt.Errorf("config extensions: duplicate instance %q", manifest.InstanceID)
		}
		instances[manifest.InstanceID] = true
	}
	return &cfg, nil
}

// NewExternalIdentityResolver builds the immutable runtime resolver from
// trusted, persistent Core configuration.
func NewExternalIdentityResolver(core CoreConfig) (*externalidentity.MemoryResolver, error) {
	return externalidentity.NewMemoryResolver(core.ExternalIdentities)
}

// Save writes a Config to a JSON file.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("config mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("config write: %w", err)
	}
	return nil
}

// FindConfig walks common paths looking for neondoll.json.
func FindConfig() (string, error) {
	candidates := []string{
		"neondoll.json",
		"config/neondoll.json",
		filepath.Join(os.Getenv("HOME"), ".neondoll", "neondoll.json"),
		"/etc/neondoll/neondoll.json",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return filepath.Abs(path)
		}
	}
	return "", fmt.Errorf("config not found in standard locations")
}
