package session_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

func TestParticipantBindingsSurviveCoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := session.New(store.(session.Store))
	created, err := first.Create(ctx, session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}, ExternalConversationRef: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.BindParticipant(ctx, created.ID, "owner-zero"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := session.New(reopened.(session.Store))
	bindings, err := second.ListParticipants(ctx, created.ID)
	if err != nil || len(bindings) != 1 || bindings[0].PrincipalID != "owner-zero" || bindings[0].SessionID != created.ID {
		t.Fatalf("reconstructed bindings = %#v, %v", bindings, err)
	}
}

func TestIdentityRevocationPreservesOtherSourceForSamePrincipal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.New(store.(session.Store))
	created, err := manager.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	first := session.ParticipantSource{SessionID: created.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "one", PrincipalID: "owner"}
	second := session.ParticipantSource{SessionID: created.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "two", PrincipalID: "owner"}
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, first); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, second); err != nil {
		t.Fatal(err)
	}
	if err = manager.ReconcileParticipantSources(ctx, []session.ParticipantSource{{GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "two", PrincipalID: "owner"}}, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	bindings, err := manager.ListParticipants(ctx, created.ID)
	if err != nil || len(bindings) != 1 || bindings[0].PrincipalID != "owner" {
		t.Fatalf("bindings=%#v err=%v", bindings, err)
	}
	if err = manager.ReconcileParticipantSources(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	bindings, err = manager.ListParticipants(ctx, created.ID)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("revoked bindings=%#v err=%v", bindings, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bindings, err = session.New(reopened.(session.Store)).ListParticipants(ctx, created.ID)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("restarted revoked bindings=%#v err=%v", bindings, err)
	}
}

func TestIdentityReassignmentRemovesOnlyObsoleteParticipantAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.New(store.(session.Store))
	created, err := manager.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "group"})
	if err != nil {
		t.Fatal(err)
	}
	source := session.ParticipantSource{SessionID: created.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "reassigned", PrincipalID: "old"}
	other := session.ParticipantSource{SessionID: created.ID, GatewayInstanceID: "gateway", Platform: "telegram", SubjectID: "member", PrincipalID: "member"}
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, source); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, other); err != nil {
		t.Fatal(err)
	}
	source.PrincipalID = "new"
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, source); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.BindResolvedParticipant(ctx, created.ID, source); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := session.New(reopened.(session.Store))
	bindings, err := restarted.ListParticipants(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		seen[binding.PrincipalID] = true
	}
	if seen["old"] || !seen["new"] || !seen["member"] || len(seen) != 2 {
		t.Fatalf("bindings=%#v", bindings)
	}
}
