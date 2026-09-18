package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SaveState serializes DollState to a JSON file.
func SaveState(dir string, s *DollState) (string, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", &StateError{Op: "marshal", Err: err}
	}
	path := filepath.Join(dir, "dollstate.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", &StateError{Op: "write", Err: err}
	}
	return path, nil
}

// LoadState deserializes DollState from a JSON file.
func LoadState(path string) (*DollState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &StateError{Op: "read", Err: err}
	}
	var s DollState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, &StateError{Op: "unmarshal", Err: err}
	}
	return &s, nil
}

// SaveConfig serializes CoreConfig to a JSON file.
func SaveConfig(dir string, c *CoreConfig) (string, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", &StateError{Op: "marshal_config", Err: err}
	}
	path := filepath.Join(dir, "coreconfig.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", &StateError{Op: "write_config", Err: err}
	}
	return path, nil
}

// LoadConfig deserializes CoreConfig.
func LoadConfig(path string) (*CoreConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &StateError{Op: "read_config", Err: err}
	}
	var c CoreConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &StateError{Op: "unmarshal_config", Err: err}
	}
	return &c, nil
}

// SaveSecrets serializes Secrets to a JSON Lines file.
func SaveSecrets(dir string, s *Secrets) (string, error) {
	path := filepath.Join(dir, "secrets.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", &StateError{Op: "create_secrets", Err: err}
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, item := range s.Items {
		if err := enc.Encode(item); err != nil {
			return "", &StateError{Op: "encode_secret", Err: err}
		}
	}
	return path, nil
}

// LoadSecrets loads Secrets from a JSONL file.
func LoadSecrets(path string) (*Secrets, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &StateError{Op: "open_secrets", Err: err}
	}
	defer f.Close()
	var s Secrets
	dec := json.NewDecoder(f)
	for dec.More() {
		var item SecretItem
		if err := dec.Decode(&item); err != nil {
			return nil, &StateError{Op: "decode_secret", Err: err}
		}
		s.Items = append(s.Items, item)
	}
	return &s, nil
}