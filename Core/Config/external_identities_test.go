package config

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
)

func TestExternalIdentityConfigurationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neondoll.json")
	configured := Defaults()
	configured.Core.ExternalIdentities = []externalidentity.Mapping{
		{
			Identity:  externalidentity.Identity{GatewayInstanceID: "telegram-main", Platform: "telegram", SubjectID: "42"},
			Principal: externalidentity.Principal{ID: "owner-zero", Authority: externalidentity.AuthorityOwner},
		},
		{
			Identity:  externalidentity.Identity{GatewayInstanceID: "desktop-main", Platform: "desktop", SubjectID: "zero"},
			Principal: externalidentity.Principal{ID: "owner-zero", Authority: externalidentity.AuthorityOwner},
		},
	}
	if err := Save(path, &configured); err != nil {
		t.Fatal(err)
	}

	for restart := 0; restart < 2; restart++ {
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("restart %d Load: %v", restart, err)
		}
		resolver, err := NewExternalIdentityResolver(loaded.Core)
		if err != nil {
			t.Fatalf("restart %d resolver: %v", restart, err)
		}
		for _, mapping := range configured.Core.ExternalIdentities {
			principal, err := resolver.Resolve(context.Background(), externalidentity.PresentedIdentity{Identity: mapping.Identity})
			if err != nil || principal.ID != "owner-zero" || principal.Authority != externalidentity.AuthorityOwner {
				t.Fatalf("restart %d resolution = %#v, %v", restart, principal, err)
			}
		}
	}
}

func TestLoadRejectsInvalidExternalIdentityMappings(t *testing.T) {
	tests := map[string][]externalidentity.Mapping{
		"malformed": {{
			Identity:  externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram"},
			Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner},
		}},
		"duplicate": {
			{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "42"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}},
			{Identity: externalidentity.Identity{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "42"}, Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner}},
		},
		"conflicting": {
			{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-a", Platform: "telegram", SubjectID: "42"}, Principal: externalidentity.Principal{ID: "principal", Authority: externalidentity.AuthorityOwner}},
			{Identity: externalidentity.Identity{GatewayInstanceID: "gateway-b", Platform: "desktop", SubjectID: "zero"}, Principal: externalidentity.Principal{ID: "principal", Authority: externalidentity.AuthorityPeer}},
		},
	}
	for name, mappings := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "neondoll.json")
			configured := Defaults()
			configured.Core.ExternalIdentities = mappings
			if err := Save(path, &configured); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); !errors.Is(err, externalidentity.ErrInvalidMapping) {
				t.Fatalf("Load error = %v", err)
			}
		})
	}
}
