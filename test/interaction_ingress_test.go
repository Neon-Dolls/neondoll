//go:build integration || e2e

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	coreruntime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

const integrationBodyID = "integration-body"

// newAuthenticatedInteractionIngress mirrors neondolld's production session
// wiring while injecting the authenticated Body identity at the test boundary.
func newAuthenticatedInteractionIngress(t *testing.T, store persistence.Store, service *interaction.Service) (*interaction.BodyIngress, ws.Config) {
	t.Helper()
	sessionStore, ok := store.(session.Store)
	if !ok {
		t.Fatal("persistence store does not implement session.Store")
	}
	services, err := coreruntime.NewServices(context.Background(), sessionStore)
	if err != nil {
		t.Fatalf("runtime services: %v", err)
	}
	router, err := services.InteractionRouter(service)
	if err != nil {
		t.Fatalf("interaction router: %v", err)
	}
	ingress, err := interaction.NewBodyIngress(services.Sessions(), router)
	if err != nil {
		t.Fatalf("Body ingress: %v", err)
	}
	config := ws.Config{
		Listen: "127.0.0.1:0",
		AuthenticateExecution: func(*http.Request) (string, error) {
			return integrationBodyID, nil
		},
	}
	return ingress, config
}
