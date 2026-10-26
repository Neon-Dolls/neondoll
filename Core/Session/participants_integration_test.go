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
