package externalidentity

import (
	"context"
	"errors"
	"testing"
)

func configuredResolver(t *testing.T) *MemoryResolver {
	t.Helper()
	resolver, err := NewMemoryResolver([]Mapping{
		{
			Identity:  Identity{GatewayInstanceID: "telegram-primary", Platform: "telegram", SubjectID: "user-42"},
			Principal: Principal{ID: "owner-zero", Authority: AuthorityOwner},
		},
		{
			Identity:  Identity{GatewayInstanceID: "discord-community", Platform: "discord", SubjectID: "member-7"},
			Principal: Principal{ID: "known-member-7", Authority: AuthorityParticipant},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestResolveMappedOwner(t *testing.T) {
	resolver := configuredResolver(t)
	principal, err := resolver.Resolve(context.Background(), PresentedIdentity{Identity: Identity{
		GatewayInstanceID: "telegram-primary", Platform: "telegram", SubjectID: "user-42",
	}})
	if err != nil || principal.ID != "owner-zero" || principal.Authority != AuthorityOwner {
		t.Fatalf("Resolve = %#v, %v", principal, err)
	}
}

func TestResolveUnmappedIdentity(t *testing.T) {
	resolver := configuredResolver(t)
	principal, err := resolver.Resolve(context.Background(), PresentedIdentity{Identity: Identity{
		GatewayInstanceID: "telegram-primary", Platform: "telegram", SubjectID: "unknown-user",
	}})
	if !errors.Is(err, ErrUnmapped) || principal != (Principal{}) {
		t.Fatalf("Resolve = %#v, %v", principal, err)
	}
}

func TestResolveRejectsWrongGateway(t *testing.T) {
	resolver := configuredResolver(t)
	_, err := resolver.Resolve(context.Background(), PresentedIdentity{Identity: Identity{
		GatewayInstanceID: "telegram-secondary", Platform: "telegram", SubjectID: "user-42",
	}})
	if !errors.Is(err, ErrUnmapped) {
		t.Fatalf("wrong Gateway error = %v", err)
	}
}

func TestForgedAuthorityClaimsCannotInfluenceResolution(t *testing.T) {
	resolver := configuredResolver(t)
	principal, err := resolver.Resolve(context.Background(), PresentedIdentity{
		Identity: Identity{GatewayInstanceID: "discord-community", Platform: "discord", SubjectID: "member-7"},
		Claims: map[string]string{
			"principal_id": "owner-zero",
			"authority":    "owner",
			"role":         "primary_peer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != "known-member-7" || principal.Authority != AuthorityParticipant {
		t.Fatalf("forged claims changed resolution: %#v", principal)
	}
}

func TestMappingValidationAndImmutability(t *testing.T) {
	mappings := []Mapping{{
		Identity:  Identity{GatewayInstanceID: "gateway", Platform: "platform", SubjectID: "subject"},
		Principal: Principal{ID: "owner", Authority: AuthorityOwner},
	}}
	resolver, err := NewMemoryResolver(mappings)
	if err != nil {
		t.Fatal(err)
	}
	mappings[0].Principal = Principal{ID: "attacker", Authority: AuthorityOwner}
	principal, err := resolver.Resolve(context.Background(), PresentedIdentity{Identity: mappings[0].Identity})
	if err != nil || principal.ID != "owner" {
		t.Fatalf("configuration mutation changed resolver: %#v, %v", principal, err)
	}

	invalid := []Mapping{{Identity: Identity{GatewayInstanceID: "gateway", Platform: "platform", SubjectID: "subject"}, Principal: Principal{ID: "owner", Authority: "admin"}}}
	if _, err := NewMemoryResolver(invalid); !errors.Is(err, ErrInvalidMapping) {
		t.Fatalf("invalid authority error = %v", err)
	}
	if _, err := NewMemoryResolver(append(mappings, mappings[0])); !errors.Is(err, ErrInvalidMapping) {
		t.Fatalf("duplicate mapping error = %v", err)
	}
}

func TestMultipleIdentitiesMaySharePrincipalButNotConflictOnAuthority(t *testing.T) {
	mappings := []Mapping{
		{Identity: Identity{GatewayInstanceID: "telegram", Platform: "telegram", SubjectID: "42"}, Principal: Principal{ID: "owner", Authority: AuthorityOwner}},
		{Identity: Identity{GatewayInstanceID: "desktop", Platform: "desktop", SubjectID: "zero"}, Principal: Principal{ID: "owner", Authority: AuthorityOwner}},
	}
	resolver, err := NewMemoryResolver(mappings)
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range mappings {
		principal, err := resolver.Resolve(context.Background(), PresentedIdentity{Identity: mapping.Identity})
		if err != nil || principal.ID != "owner" || principal.Authority != AuthorityOwner {
			t.Fatalf("Resolve(%+v) = %#v, %v", mapping.Identity, principal, err)
		}
	}

	mappings[1].Principal.Authority = AuthorityPeer
	if _, err := NewMemoryResolver(mappings); !errors.Is(err, ErrInvalidMapping) {
		t.Fatalf("conflicting principal authority error = %v", err)
	}
}

func TestListPrincipalsCollapsesIdentitiesAndIsDeterministic(t *testing.T) {
	resolver, err := NewMemoryResolver([]Mapping{
		{Identity: Identity{GatewayInstanceID: "telegram-b", Platform: "telegram", SubjectID: "42"}, Principal: Principal{ID: "owner", Authority: AuthorityOwner}},
		{Identity: Identity{GatewayInstanceID: "matrix-a", Platform: "matrix", SubjectID: "zero"}, Principal: Principal{ID: "owner", Authority: AuthorityOwner}},
		{Identity: Identity{GatewayInstanceID: "gateway", Platform: "test", SubjectID: "7"}, Principal: Principal{ID: "peer", Authority: AuthorityPeer}},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := resolver.ListPrincipals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Principal.ID != "owner" || records[1].Principal.ID != "peer" {
		t.Fatalf("records=%#v", records)
	}
	if len(records[0].Identities) != 2 || records[0].Identities[0].Platform != "matrix" || records[0].Identities[1].Platform != "telegram" {
		t.Fatalf("owner identities=%#v", records[0].Identities)
	}

	// Resolver replacement is the existing configuration reload boundary. A
	// removed mapping is not discoverable from the replacement directory.
	reloaded, err := NewMemoryResolver([]Mapping{{
		Identity:  Identity{GatewayInstanceID: "gateway", Platform: "test", SubjectID: "7"},
		Principal: Principal{ID: "peer", Authority: AuthorityPeer},
	}})
	if err != nil {
		t.Fatal(err)
	}
	records, err = reloaded.ListPrincipals(context.Background())
	if err != nil || len(records) != 1 || records[0].Principal.ID != "peer" {
		t.Fatalf("reloaded records=%#v err=%v", records, err)
	}
}
