package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Extension"
	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// TestTelegramGatewayProductionInteractionPath runs only when supplied the
// independently built Telegram artifact. It exercises the same Extension
// Manager, Gateway runtime, Session router, Interaction service, context
// builder, durable store, and ABI v2 adapter composed by neondolld.
func TestTelegramGatewayProductionInteractionPath(t *testing.T) {
	artifactPath := os.Getenv("NEONDOLL_TELEGRAM_WASM")
	if artifactPath == "" {
		t.Skip("set NEONDOLL_TELEGRAM_WASM to the independently built Telegram artifact")
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	sendAttempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/sendMessage") {
			mu.Lock()
			sendAttempts++
			attempt := sendAttempts
			mu.Unlock()
			if attempt == 1 {
				_, _ = writer.Write([]byte(`{"ok":false,"error_code":500,"description":"temporary"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
			return
		}
		if request.URL.Query().Get("offset") == "43" {
			_, _ = writer.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true,"result":[{"update_id":42,"message":{"message_id":1,"from":{"id":9},"chat":{"id":7,"type":"private"},"date":1,"text":"hello doll"}}]}`))
	}))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	parsed, _ := url.Parse(server.URL)

	root := t.TempDir()
	store, err := persistence.NewStore(filepath.Join(root, "neondoll.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := dollstate.DollState{Version: dollstate.CurrentStateVersion}
	state.Identity.DollID = "test-doll"
	state.Identity.CanonicalName = "Test Doll"
	state.Soul.Content = "Answer clearly."
	if err = store.SaveDoll(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	resolver, err := externalidentity.NewMemoryResolver([]externalidentity.Mapping{{
		Identity:  externalidentity.Identity{GatewayInstanceID: "telegram-e2e", Platform: "telegram", SubjectID: "9"},
		Principal: externalidentity.Principal{ID: "owner", Authority: externalidentity.AuthorityOwner},
	}})
	if err != nil {
		t.Fatal(err)
	}
	services, err := runtime.NewServices(context.Background(), store.(session.Store), runtime.WithExternalIdentityResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	model := config.ModelConfiguration{
		Name: "test", Provider: "openai", Model: "test-model", Endpoint: "http://unused",
		Budget: inference.ModelContextBudget{ContextTokens: 9216, OutputTokens: 768, RequiredOverheadTokens: 256},
	}
	interactionService := interaction.New(store, inference.NewMockProvider("mock", "hello from the doll"), logger.New(logger.ErrorLevel, io.Discard), interaction.WithModelConfiguration(model))
	router, err := services.InteractionRouter(interactionService)
	if err != nil {
		t.Fatal(err)
	}
	gatewayRuntime, err := runtime.NewGatewayRuntime(context.Background(), services, store.(sessionevent.Store), store.(permissionrequest.Store), resolver, router, actionpolicy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayRuntime.Shutdown(context.Background())

	artifactCopy := filepath.Join(root, "telegram.wasm")
	if err = os.WriteFile(artifactCopy, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	secretsRoot := filepath.Join(root, "secrets")
	if err = os.MkdirAll(secretsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(secretsRoot, "token"), []byte("123:test"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	configuration, _ := json.Marshal(map[string]any{
		"version": 1, "telegram": map[string]any{"host": server.URL},
		"polling": map[string]any{"timeout_seconds": 1, "allowed_updates": []string{"message"}},
	})
	manifest := extension.Manifest{
		Version: 1, ExtensionID: "telegram", InstanceID: "telegram-e2e", Contract: extension.ContractGateway, ABI: 2, Enabled: true,
		Artifact: extension.Artifact{Path: "telegram.wasm", SHA256: fmt.Sprintf("%x", sum)}, Configuration: configuration,
		Requested:     []extension.Capability{extension.CapabilityHTTP, extension.CapabilitySecrets, extension.CapabilityState, extension.CapabilityLog},
		Granted:       []extension.Capability{extension.CapabilityHTTP, extension.CapabilitySecrets, extension.CapabilityState, extension.CapabilityLog},
		HTTPAllowlist: []string{parsed.Hostname()}, Secrets: map[string]string{"telegram_token": "token"},
		Gateway: &extension.GatewayConfiguration{Platform: "telegram", DollID: "test-doll", ConversationPrefix: "telegram/chat/"},
	}
	wasmRuntime, err := extension.NewWASMRuntime(context.Background(), extension.NewFileStateStore(filepath.Join(root, "extension-state")), secretsRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := extension.NewManager(wasmRuntime, gatewayRuntime, root, []extension.Manifest{manifest})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	if err = manager.LoadEnabled(context.Background()); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		mu.Lock()
		attempts := sendAttempts
		mu.Unlock()
		if attempts >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Telegram response was not retried; send attempts=%d", attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	active, err := services.Sessions().ListActive(context.Background(), nil)
	if err != nil || len(active) != 1 || active[0].Owner.InstanceID != "telegram-e2e" || active[0].ExternalConversationRef != "telegram/chat/7" {
		t.Fatalf("active sessions=%#v error=%v", active, err)
	}
	history, err := store.(contextbuilder.HistoryStore).ListSessionMessages(context.Background(), active[0].ID)
	if err != nil || len(history) != 2 || history[0].Content != "hello doll" || history[1].Content != "hello from the doll" {
		t.Fatalf("history=%#v error=%v", history, err)
	}
	participants, err := services.Sessions().(session.ParticipantService).ListParticipants(context.Background(), active[0].ID)
	if err != nil || len(participants) != 1 || participants[0].PrincipalID != "owner" {
		t.Fatalf("participants=%#v error=%v", participants, err)
	}
	// The mock endpoint continues returning update 42 until its durable ack is
	// observed. No duplicate interaction turn may be persisted.
	time.Sleep(300 * time.Millisecond)
	history, err = store.(contextbuilder.HistoryStore).ListSessionMessages(context.Background(), active[0].ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("duplicate interaction history=%#v error=%v", history, err)
	}
}
