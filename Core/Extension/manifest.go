package extension

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	ManifestVersion       = 1
	GuestABIVersion       = 2
	ContractGateway       = "gateway"
	ContractCapability    = "capability"
	ContractPackageSource = "package_source"
)

type Capability string

const (
	CapabilityHTTP    Capability = "http"
	CapabilitySecrets Capability = "secrets"
	CapabilityState   Capability = "state"
	CapabilityLog     Capability = "log"
)

var (
	ErrInvalidManifest     = errors.New("extension: invalid manifest")
	ErrUnsupportedContract = errors.New("extension: unsupported contract")
	ErrUnsupportedABI      = errors.New("extension: unsupported ABI")
	ErrCapabilityDenied    = errors.New("extension: host capability denied")
)

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type GatewayConfiguration struct {
	Platform           string `json:"platform"`
	DollID             string `json:"doll_id"`
	ConversationPrefix string `json:"conversation_prefix"`
}

type Manifest struct {
	Version       int                   `json:"version"`
	ExtensionID   string                `json:"extension_id"`
	InstanceID    string                `json:"instance_id"`
	Contract      string                `json:"contract"`
	ABI           int                   `json:"abi_version"`
	Enabled       bool                  `json:"enabled"`
	Artifact      Artifact              `json:"artifact"`
	Configuration json.RawMessage       `json:"configuration,omitempty"`
	Requested     []Capability          `json:"requested_capabilities,omitempty"`
	Granted       []Capability          `json:"granted_capabilities,omitempty"`
	HTTPAllowlist []string              `json:"http_allowlist,omitempty"`
	Secrets       map[string]string     `json:"secrets,omitempty"`
	Gateway       *GatewayConfiguration `json:"gateway,omitempty"`
}

func (m Manifest) Validate() error {
	if m.Version != ManifestVersion || !validID(m.ExtensionID) || !validID(m.InstanceID) ||
		strings.TrimSpace(m.Artifact.Path) == "" || len(m.Artifact.SHA256) != 64 {
		return ErrInvalidManifest
	}
	if _, err := hex.DecodeString(m.Artifact.SHA256); err != nil {
		return ErrInvalidManifest
	}
	if m.Contract != ContractGateway {
		if m.Contract == ContractCapability || m.Contract == ContractPackageSource {
			return ErrUnsupportedContract
		}
		return ErrInvalidManifest
	}
	if m.ABI != GuestABIVersion {
		return ErrUnsupportedABI
	}
	if m.Gateway == nil || strings.TrimSpace(m.Gateway.Platform) == "" || strings.TrimSpace(m.Gateway.DollID) == "" || strings.TrimSpace(m.Gateway.ConversationPrefix) == "" {
		return ErrInvalidManifest
	}
	for name, path := range m.Secrets {
		if strings.TrimSpace(name) == "" || filepath.IsAbs(path) || path == "" || strings.HasPrefix(filepath.Clean(path), "..") {
			return ErrInvalidManifest
		}
	}
	requested := map[Capability]bool{}
	for _, capability := range m.Requested {
		switch capability {
		case CapabilityHTTP, CapabilitySecrets, CapabilityState, CapabilityLog:
		default:
			return ErrInvalidManifest
		}
		requested[capability] = true
	}
	for _, grant := range m.Granted {
		if !requested[grant] {
			return fmt.Errorf("%w: unrequested grant %q", ErrInvalidManifest, grant)
		}
		switch grant {
		case CapabilityHTTP, CapabilitySecrets, CapabilityState, CapabilityLog:
		default:
			return ErrInvalidManifest
		}
	}
	return nil
}

func validID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func ValidateManifests(manifests []Manifest) error {
	instances := map[string]bool{}
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("%s: %w", manifest.InstanceID, err)
		}
		if instances[manifest.InstanceID] {
			return fmt.Errorf("%w: duplicate instance %q", ErrInvalidManifest, manifest.InstanceID)
		}
		instances[manifest.InstanceID] = true
	}
	return nil
}

func (m Manifest) GrantedCapability(capability Capability) bool {
	for _, grant := range m.Granted {
		if grant == capability {
			return true
		}
	}
	return false
}
