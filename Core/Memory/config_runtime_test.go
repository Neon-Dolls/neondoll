package memory

import (
	"errors"
	"testing"
)

func TestRuntimeProviderConfigurationValidation(t *testing.T) {
	tests := []struct {
		name    string
		ref     ProviderReference
		wantErr bool
	}{
		{"sqlite", ProviderReference{InstanceID: "one", ContractVersion: 1, Implementation: ImplementationSQLite, SQLite: &SQLiteConfiguration{Path: "memory.db"}}, false},
		{"openviking", ProviderReference{InstanceID: "one", ContractVersion: 1, Implementation: ImplementationOpenViking, OpenViking: &OpenVikingConfiguration{Endpoint: "http://memory", UserID: "user"}}, false},
		{"hindsight", ProviderReference{InstanceID: "one", ContractVersion: 1, Implementation: ImplementationHindsight, Hindsight: &HindsightConfiguration{Endpoint: "http://memory"}}, false},
		{"unknown", ProviderReference{InstanceID: "one", ContractVersion: 1, Implementation: "other", SQLite: &SQLiteConfiguration{Path: "memory.db"}}, true},
		{"mismatched settings", ProviderReference{InstanceID: "one", ContractVersion: 1, Implementation: ImplementationSQLite, Hindsight: &HindsightConfiguration{Endpoint: "http://memory"}}, true},
		{"legacy module reference", ProviderReference{InstanceID: "one", ContractVersion: 1}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Configuration{Active: "primary", Providers: map[string]ProviderReference{"primary": test.ref}}
			err := cfg.Validate()
			if (err != nil) != test.wantErr || (err != nil && !errors.Is(err, ErrInvalidConfiguration)) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}
