// Package referencebody provides a minimal persistent Body used only by the
// integration suite. It deliberately composes production protocol packages
// instead of reimplementing their transcripts or execution semantics.
package referencebody

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	bodyruntime "github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

const stateVersion = 1

type State struct {
	Version           int                            `json:"version"`
	SigningPrivateKey string                         `json:"signing_private_key"`
	WireGuardPrivate  string                         `json:"wireguard_private_key"`
	CoreID            string                         `json:"core_id,omitempty"`
	CorePublicKey     string                         `json:"core_public_key,omitempty"`
	RequestID         string                         `json:"request_id,omitempty"`
	Receipt           *dollnetwork.EnrollmentReceipt `json:"receipt,omitempty"`
	Metadata          dollnetwork.EnrollmentMetadata `json:"metadata,omitempty"`
}

func LoadOrCreate(path string) (*State, error) {
	encoded, err := os.ReadFile(path)
	if err == nil {
		var state State
		if err := json.Unmarshal(encoded, &state); err != nil {
			return nil, fmt.Errorf("reference Body: decode state: %w", err)
		}
		if err := state.validate(); err != nil {
			return nil, err
		}
		return &state, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, signingPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	wg, err := bodyruntime.GenerateWgKeypair()
	if err != nil {
		return nil, err
	}
	wgPrivate := wg.PrivateKeyBytes()
	defer bodyruntime.Wipe(wgPrivate)
	state := &State{
		Version: stateVersion, SigningPrivateKey: base64.RawURLEncoding.EncodeToString(signingPrivate),
		WireGuardPrivate: base64.RawURLEncoding.EncodeToString(wgPrivate),
		Metadata:         dollnetwork.EnrollmentMetadata{"name": "reference-body"},
	}
	if err := state.Save(path); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *State) Save(path string) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (s *State) SigningPrivate() (ed25519.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s.SigningPrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("reference Body: invalid persisted Ed25519 private key")
	}
	return ed25519.PrivateKey(raw), nil
}

func (s *State) SigningPublic() (ed25519.PublicKey, error) {
	privateKey, err := s.SigningPrivate()
	if err != nil {
		return nil, err
	}
	return privateKey.Public().(ed25519.PublicKey), nil
}

func (s *State) CorePublic() (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s.CorePublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("reference Body: invalid persisted Core public key")
	}
	return ed25519.PublicKey(raw), nil
}

func (s *State) WireGuardKeypair() (*bodyruntime.WgKeypair, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s.WireGuardPrivate)
	if err != nil {
		return nil, errors.New("reference Body: invalid persisted WireGuard private key")
	}
	defer bodyruntime.Wipe(raw)
	return bodyruntime.NewWgKeypair(raw)
}

func (s *State) BodyID() string {
	if s.Receipt == nil {
		return ""
	}
	return s.Receipt.BodyID
}

func (s *State) validate() error {
	if s == nil || s.Version != stateVersion {
		return errors.New("reference Body: unsupported state version")
	}
	if _, err := s.SigningPrivate(); err != nil {
		return err
	}
	if _, err := s.WireGuardKeypair(); err != nil {
		return err
	}
	return nil
}
