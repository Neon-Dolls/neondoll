//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/test/referencebody"
)

const referenceProcessRole = "NEONDOLL_REFERENCE_PROCESS_ROLE"

type referenceCoreReady struct {
	BaseURL       string `json:"base_url"`
	WebSocketURL  string `json:"websocket_url"`
	AdminSocket   string `json:"admin_socket"`
	CoreID        string `json:"core_id"`
	CorePublicKey string `json:"core_public_key"`
}

type referenceBodyStatus struct {
	ApprovalCode string `json:"approval_code,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	BodyID       string `json:"body_id,omitempty"`
	Connected    bool   `json:"connected"`
	Connections  int64  `json:"connections"`
	Attempts     int64  `json:"attempts"`
	LastError    string `json:"last_error,omitempty"`
}

// TestReferenceBodyProcess is the subprocess entry point. The parent test
// starts separate Core and Body OS processes from the integration test binary.
func TestReferenceBodyProcess(t *testing.T) {
	switch os.Getenv(referenceProcessRole) {
	case "core":
		if err := runReferenceCoreProcess(); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case "body":
		if err := runReferenceBodyProcess(); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	default:
		t.Skip("subprocess helper")
	}
}

type allowReferenceEcho struct{}

func (allowReferenceEcho) Evaluate(request corebody.AuthorityRequest) corebody.AuthorityResult {
	if request.Capability == "test.echo" && request.Operation == "echo" {
		return corebody.AuthorityResult{Decision: corebody.AuthorityAllow}
	}
	return corebody.AuthorityResult{Decision: corebody.AuthorityDeny, Reason: "not_allowed"}
}

func runReferenceCoreProcess() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	stateDir := os.Getenv("NEONDOLL_REFERENCE_CORE_STATE")
	readyPath := os.Getenv("NEONDOLL_REFERENCE_CORE_READY")
	if stateDir == "" || readyPath == "" {
		return errors.New("reference Core: state and ready paths are required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	store, err := persistence.NewStore(filepath.Join(stateDir, "neondoll.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	networkStore := store.(network.NetworkStore)
	nw, err := networkStore.LoadNetwork(ctx)
	if err != nil {
		return err
	}
	if nw == nil {
		nw, err = network.NewNetwork(network.GenerateNetworkID())
		if err != nil {
			return err
		}
		if err := networkStore.SaveNetwork(ctx, nw); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	registry := corebody.NewRegistry()
	server := ws.New(ws.Config{}, referencebody.Logger{}, nil)
	invitationService := invitation.NewService(store.(invitation.Store), invitation.RealClock{})
	ownerBoundary := ownercontrol.NewBoundary()
	enrollmentService, err := enrollment.NewService(ctx, store.(enrollment.Store), nw, invitationService, ownerBoundary, nil, enrollment.DefaultConfig())
	if err != nil {
		return err
	}
	enrollment.RegisterBodyEndpoints(server, enrollmentService)
	server.SetBodyAuthenticator(enrollmentService)
	registrar, err := corebody.NewRemoteRegistrar(registry, networkStore, server)
	if err != nil {
		return err
	}
	server.SetExecutionSessionLifecycle(registrar)
	enrollmentService.SetRevocationLifecycle(server, nil)
	guard := corebody.NewGuard(registry, allowReferenceEcho{})
	server.RegisterHandler("/reference/execute", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var value corebody.ExecutionRequest
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(guard.ExecuteContext(request.Context(), value))
	})
	server.SetListener(listener)
	ownerSocket := filepath.Join(stateDir, "neondoll-admin.sock")
	ownerServer, err := ownercontrol.NewServer(ownerSocket, ownercontrol.NewService(enrollmentService, invitationService, ownerBoundary))
	if err != nil {
		return err
	}
	serveOwner := make(chan error, 1)
	go func() { serveOwner <- ownerServer.Serve() }()
	serveWebSocket := make(chan error, 1)
	go func() { serveWebSocket <- server.Start(ctx) }()
	identity := enrollmentService.CoreIdentity()
	ready := referenceCoreReady{
		BaseURL: "http://" + listener.Addr().String(), WebSocketURL: "ws://" + listener.Addr().String() + "/ws",
		AdminSocket: ownerSocket, CoreID: identity.CoreID, CorePublicKey: base64.RawURLEncoding.EncodeToString(identity.PublicKey),
	}
	if err := writeReferenceJSON(readyPath, ready); err != nil {
		return err
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = ownerServer.Shutdown(shutdownCtx)
	_ = server.Shutdown(shutdownCtx)
	select {
	case err := <-serveOwner:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	default:
	}
	select {
	case err := <-serveWebSocket:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	default:
	}
	return ctx.Err()
}

func runReferenceBodyProcess() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	statePath := os.Getenv("NEONDOLL_REFERENCE_BODY_STATE")
	statusPath := os.Getenv("NEONDOLL_REFERENCE_BODY_STATUS")
	readyPath := os.Getenv("NEONDOLL_REFERENCE_CORE_READY")
	client, err := referencebody.New(statePath)
	if err != nil {
		return err
	}
	var status referenceBodyStatus
	if client.State.Receipt == nil {
		ready, err := waitReferenceJSON[referenceCoreReady](ctx, readyPath, func(value referenceCoreReady) bool { return value.CoreID != "" })
		if err != nil {
			return err
		}
		coreKey, err := decodeReferenceCoreKey(ready.CorePublicKey)
		if err != nil {
			return err
		}
		pending, err := client.BeginEnrollment(ctx, ready.BaseURL, ready.CoreID, coreKey)
		if err != nil {
			return err
		}
		status.ApprovalCode, status.RequestID = pending.ApprovalCode, pending.RequestID
		if err := writeReferenceJSON(statusPath, status); err != nil {
			return err
		}
		for client.State.Receipt == nil {
			if _, err := client.PollEnrollment(ctx, ready.BaseURL); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	status.BodyID = client.State.BodyID()
	for ctx.Err() == nil {
		ready, err := waitReferenceJSON[referenceCoreReady](ctx, readyPath, func(value referenceCoreReady) bool {
			return value.WebSocketURL != "" && value.CoreID == client.State.CoreID && value.CorePublicKey == client.State.CorePublicKey
		})
		if err != nil {
			return err
		}
		status.Attempts++
		status.Connected = false
		status.LastError = ""
		_ = writeReferenceJSON(statusPath, status)
		err = client.Connect(ctx, ready.WebSocketURL, func(bodyID string) {
			status.BodyID, status.Connected, status.LastError = bodyID, true, ""
			status.Connections++
			_ = writeReferenceJSON(statusPath, status)
		})
		status.Connected = false
		if err != nil && ctx.Err() == nil {
			status.LastError = err.Error()
		}
		_ = writeReferenceJSON(statusPath, status)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return ctx.Err()
}

func TestReferenceBodyEnrollmentAuthenticationRestartAndRevocation(t *testing.T) {
	temporary := t.TempDir()
	coreState := filepath.Join(temporary, "core")
	bodyState := filepath.Join(temporary, "body", "identity.json")
	coreReadyPath := filepath.Join(temporary, "core-ready.json")
	bodyStatusPath := filepath.Join(temporary, "body-status.json")

	coreProcess := startReferenceProcess(t, "core", map[string]string{
		"NEONDOLL_REFERENCE_CORE_STATE": coreState, "NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	ready := awaitReferenceJSON(t, coreReadyPath, func(value referenceCoreReady) bool { return value.CoreID != "" })
	bodyProcess := startReferenceProcess(t, "body", map[string]string{
		"NEONDOLL_REFERENCE_BODY_STATE": bodyState, "NEONDOLL_REFERENCE_BODY_STATUS": bodyStatusPath,
		"NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	pending := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.ApprovalCode != "" })
	if _, err := ownercontrol.NewClient(ready.AdminSocket).Approve(context.Background(), pending.ApprovalCode, []string{"test.echo"}); err != nil {
		t.Fatal(err)
	}
	connected := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.Connected && value.BodyID != "" })
	bodyID := connected.BodyID
	assertReferenceEcho(t, ready.BaseURL, bodyID, "initial")
	originalState := awaitReferenceJSON(t, bodyState, func(value referencebody.State) bool { return value.BodyID() != "" })

	stopReferenceProcess(t, bodyProcess)
	_ = os.Remove(bodyStatusPath)
	bodyProcess = startReferenceProcess(t, "body", map[string]string{
		"NEONDOLL_REFERENCE_BODY_STATE": bodyState, "NEONDOLL_REFERENCE_BODY_STATUS": bodyStatusPath,
		"NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	connected = awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.Connected })
	if connected.BodyID != bodyID {
		t.Fatalf("Body identity changed after restart: %q != %q", connected.BodyID, bodyID)
	}
	restartedState := awaitReferenceJSON(t, bodyState, func(value referencebody.State) bool { return value.BodyID() != "" })
	if restartedState.SigningPrivateKey != originalState.SigningPrivateKey || restartedState.WireGuardPrivate != originalState.WireGuardPrivate {
		t.Fatal("Body signing or WireGuard identity changed after restart")
	}
	assertReferenceEcho(t, ready.BaseURL, bodyID, "body-restart")

	stopReferenceProcess(t, coreProcess)
	_ = os.Remove(coreReadyPath)
	coreProcess = startReferenceProcess(t, "core", map[string]string{
		"NEONDOLL_REFERENCE_CORE_STATE": coreState, "NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	restartedCore := awaitReferenceJSON(t, coreReadyPath, func(value referenceCoreReady) bool { return value.CoreID != "" })
	if restartedCore.CoreID != ready.CoreID || restartedCore.CorePublicKey != ready.CorePublicKey {
		t.Fatal("Core enrollment identity changed after restart")
	}
	connectionsBefore := connected.Connections
	connected = awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool {
		return value.Connected && value.Connections > connectionsBefore
	})
	assertReferenceEcho(t, restartedCore.BaseURL, bodyID, "core-restart")

	attemptsBefore := connected.Attempts
	if err := ownercontrol.NewClient(restartedCore.AdminSocket).Revoke(context.Background(), bodyID); err != nil {
		t.Fatal(err)
	}
	rejected := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool {
		return !value.Connected && value.Attempts > attemptsBefore && value.LastError != ""
	})
	if rejected.BodyID != bodyID {
		t.Fatalf("revoked reconnect used Body %q, want %q", rejected.BodyID, bodyID)
	}
	result := executeReference(t, restartedCore.BaseURL, bodyID, "after-revocation")
	if result.Status == corebody.StatusSuccess {
		t.Fatal("revoked Body executed successfully")
	}

	stopReferenceProcess(t, bodyProcess)
	stopReferenceProcess(t, coreProcess)
}

type referenceProcess struct {
	command *exec.Cmd
	done    chan error
}

func startReferenceProcess(t *testing.T, role string, environment map[string]string) *referenceProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestReferenceBodyProcess$")
	command.Env = append(os.Environ(), referenceProcessRole+"="+role)
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	logPath := filepath.Join(t.TempDir(), role+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		logFile.Close()
	}()
	process := &referenceProcess{command: command, done: done}
	t.Cleanup(func() {
		if command.ProcessState != nil {
			return
		}
		if command.Process != nil {
			_ = command.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if command.Process != nil {
				_ = command.Process.Kill()
			}
		}
	})
	return process
}

func stopReferenceProcess(t *testing.T, process *referenceProcess) {
	t.Helper()
	if process == nil || process.command.Process == nil {
		return
	}
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	select {
	case err := <-process.done:
		if err != nil {
			t.Fatalf("reference subprocess failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = process.command.Process.Kill()
		t.Fatal("reference subprocess did not stop")
	}
}

func assertReferenceEcho(t *testing.T, baseURL, bodyID, message string) {
	t.Helper()
	result := executeReference(t, baseURL, bodyID, message)
	var output map[string]any
	if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
		t.Fatalf("decode echo output %q: %v", result.Output, err)
	}
	if result.Status != corebody.StatusSuccess || result.ExecutionID == "" || output["echo"] != message {
		t.Fatalf("echo result = %#v", result)
	}
}

func executeReference(t *testing.T, baseURL, bodyID, message string) corebody.ExecutionResult {
	t.Helper()
	request := corebody.ExecutionRequest{
		ExecutionID: "execution-" + message, Doll: "reference-doll", Body: corebody.BodyID(bodyID),
		Capability: "test.echo", Operation: "echo", Arguments: map[string]any{"message": message},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(baseURL+"/reference/execute", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result corebody.ExecutionResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeReferenceCoreKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("reference Body: invalid Core public key")
	}
	return ed25519.PublicKey(raw), nil
}

func writeReferenceJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", path, atomic.AddUint64(&referenceWriteSequence, 1))
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

var referenceWriteSequence uint64

func awaitReferenceJSON[T any](t *testing.T, path string, accept func(T) bool) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	value, err := waitReferenceJSON(ctx, path, accept)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func waitReferenceJSON[T any](ctx context.Context, path string, accept func(T) bool) (T, error) {
	var zero T
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		encoded, err := os.ReadFile(path)
		if err == nil {
			var value T
			if json.Unmarshal(encoded, &value) == nil && accept(value) {
				return value, nil
			}
		}
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("wait for %s: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}
