package extension

import (
	"errors"
	"testing"
)

func TestMemoryProviderContractIsReservedButNotRunnable(t *testing.T) {
	manifest := Manifest{
		Version: ManifestVersion, ExtensionID: "memory.sqlite", InstanceID: "memory-primary",
		Contract: ContractMemoryProvider, ABI: GuestABIVersion,
		Artifact: Artifact{Path: "memory.wasm", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if err := manifest.Validate(); !errors.Is(err, ErrUnsupportedContract) {
		t.Fatalf("memory provider reservation: %v", err)
	}
}
