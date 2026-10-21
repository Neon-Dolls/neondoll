//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/test/referencebody"
)

type daemonBinaries struct {
	daemon string
	ctl    string
}

func TestProductionDaemonBodyLifecycleAcceptance(t *testing.T) {
	temporary := t.TempDir()
	binaries := buildDaemonAcceptanceBinaries(t, temporary)
	coreDir := filepath.Join(temporary, "core")
	bodyState := filepath.Join(temporary, "body", "identity.json")
	bodyStatusPath := filepath.Join(temporary, "body-status.json")
	coreReadyPath := filepath.Join(temporary, "core-ready.json")
	acceptanceSocket := filepath.Join(coreDir, "acceptance.sock")
	adminSocket := filepath.Join(coreDir, "data", "neondoll-admin.sock")
	listenAddress := reserveTCPAddress(t)
	writeDaemonConfig(t, coreDir, listenAddress)

	daemon := startDaemonAcceptanceProcess(t, binaries.daemon, coreDir, acceptanceSocket)
	ready := publishDaemonReady(t, coreDir, listenAddress, adminSocket, coreReadyPath)
	initialIdentity, _, err := referencebody.DiscoverCoreIdentity(context.Background(), nil, ready.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	assertUnauthenticatedAdvertisementRejected(t, ready.WebSocketURL, "body_unenrolled")
	unknown := executeDaemonAcceptance(t, acceptanceSocket, "body_unenrolled", "pre-auth")
	if unknown.Status == corebody.StatusSuccess {
		t.Fatal("unauthenticated unknown Body executed")
	}

	bodyProcess := startReferenceProcess(t, "body", map[string]string{
		"NEONDOLL_REFERENCE_BODY_STATE": bodyState, "NEONDOLL_REFERENCE_BODY_STATUS": bodyStatusPath,
		"NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	pending := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.ApprovalCode != "" })
	runNeondollctl(t, binaries.ctl, adminSocket, "body", "approve", pending.ApprovalCode, "-permissions", "test.echo")
	connected := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.Connected && value.BodyID != "" })
	bodyID := connected.BodyID
	assertDaemonEcho(t, acceptanceSocket, bodyID, "initial")
	originalState := awaitReferenceJSON(t, bodyState, func(value referencebody.State) bool { return value.BodyID() != "" })

	stopReferenceProcess(t, bodyProcess)
	awaitDaemonExecutionStatus(t, acceptanceSocket, bodyID, corebody.StatusUnavailable)
	assertUnauthenticatedAdvertisementRejected(t, ready.WebSocketURL, bodyID)
	result := executeDaemonAcceptance(t, acceptanceSocket, bodyID, "unauthenticated")
	if result.Status == corebody.StatusSuccess {
		t.Fatal("unauthenticated connection made the disconnected Body executable")
	}

	_ = os.Remove(bodyStatusPath)
	bodyProcess = startReferenceProcess(t, "body", map[string]string{
		"NEONDOLL_REFERENCE_BODY_STATE": bodyState, "NEONDOLL_REFERENCE_BODY_STATUS": bodyStatusPath,
		"NEONDOLL_REFERENCE_CORE_READY": coreReadyPath,
	})
	connected = awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool { return value.Connected })
	if connected.BodyID != bodyID {
		t.Fatalf("Body identity changed after process restart: %q != %q", connected.BodyID, bodyID)
	}
	restartedState := awaitReferenceJSON(t, bodyState, func(value referencebody.State) bool { return value.BodyID() != "" })
	if restartedState.SigningPrivateKey != originalState.SigningPrivateKey || restartedState.WireGuardPrivate != originalState.WireGuardPrivate {
		t.Fatal("Body cryptographic identity changed after restart")
	}
	assertDaemonEcho(t, acceptanceSocket, bodyID, "body-restart")

	stopDaemonAcceptanceProcess(t, daemon)
	_ = os.Remove(coreReadyPath)
	daemon = startDaemonAcceptanceProcess(t, binaries.daemon, coreDir, acceptanceSocket)
	restartedCore := publishDaemonReady(t, coreDir, listenAddress, adminSocket, coreReadyPath)
	restartedIdentity, _, err := referencebody.DiscoverCoreIdentity(context.Background(), nil, restartedCore.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if restartedIdentity != initialIdentity {
		t.Fatal("Core signing identity changed after daemon restart")
	}
	connectionsBefore := connected.Connections
	connected = awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool {
		return value.Connected && value.Connections > connectionsBefore
	})
	assertDaemonEcho(t, acceptanceSocket, bodyID, "core-restart")

	attemptsBefore := connected.Attempts
	runNeondollctl(t, binaries.ctl, adminSocket, "body", "revoke", bodyID)
	rejected := awaitReferenceJSON(t, bodyStatusPath, func(value referenceBodyStatus) bool {
		return !value.Connected && value.Attempts > attemptsBefore && value.LastError != ""
	})
	if rejected.BodyID != bodyID {
		t.Fatalf("revoked reconnect used Body %q, want %q", rejected.BodyID, bodyID)
	}
	result = executeDaemonAcceptance(t, acceptanceSocket, bodyID, "revoked")
	if result.Status == corebody.StatusSuccess {
		t.Fatal("revoked Body executed successfully")
	}

	stopReferenceProcess(t, bodyProcess)
	stopDaemonAcceptanceProcess(t, daemon)
}

func buildDaemonAcceptanceBinaries(t *testing.T, destination string) daemonBinaries {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Dir(filepath.Dir(filename))
	binaries := daemonBinaries{daemon: filepath.Join(destination, "neondolld"), ctl: filepath.Join(destination, "neondollctl")}
	buildArguments := []string{"build"}
	if raceDetectorEnabled {
		buildArguments = append(buildArguments, "-race")
	}
	buildArguments = append(buildArguments, "-tags=acceptance", "-o", binaries.daemon, "./cmd/neondolld")
	command := exec.Command("go", buildArguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build neondolld: %v\n%s", err, output)
	}
	command = exec.Command("go", "build", "-o", binaries.ctl, "./cmd/neondollctl")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build neondollctl: %v\n%s", err, output)
	}
	return binaries
}

func writeDaemonConfig(t *testing.T, directory, listenAddress string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Paths.DataDir = filepath.Join(directory, "data")
	cfg.Paths.StateDir = filepath.Join(directory, "state")
	cfg.Paths.SecretsDir = filepath.Join(directory, "secrets")
	cfg.HTTP.Enabled = true
	cfg.HTTP.Listen = listenAddress
	cfg.Link.WebSocket.Listen = listenAddress
	cfg.Core.Pulse.Enabled = false
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(directory, "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func publishDaemonReady(t *testing.T, coreDir, listenAddress, adminSocket, readyPath string) referenceCoreReady {
	t.Helper()
	waitForUnixSocket(t, adminSocket)
	waitForUnixSocket(t, filepath.Join(coreDir, "acceptance.sock"))
	ready := referenceCoreReady{
		BaseURL: "http://" + listenAddress, WebSocketURL: "ws://" + listenAddress + "/ws", AdminSocket: adminSocket,
	}
	awaitDiscoveredCoreIdentity(t, ready.BaseURL)
	if err := writeReferenceJSON(readyPath, ready); err != nil {
		t.Fatal(err)
	}
	return ready
}

func awaitDiscoveredCoreIdentity(t *testing.T, baseURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, _, err := referencebody.DiscoverCoreIdentity(ctx, nil, baseURL); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Core identity discovery: %v", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type daemonAcceptanceProcess struct {
	command *exec.Cmd
	done    chan error
	logPath string
}

func startDaemonAcceptanceProcess(t *testing.T, binary, directory, acceptanceSocket string) *daemonAcceptanceProcess {
	t.Helper()
	logPath := filepath.Join(directory, fmt.Sprintf("daemon-%d.log", time.Now().UnixNano()))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	command.Dir = directory
	command.Env = append(os.Environ(), "NEONDOLL_ACCEPTANCE_SOCKET="+acceptanceSocket)
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
	process := &daemonAcceptanceProcess{command: command, done: done, logPath: logPath}
	t.Cleanup(func() {
		if command.ProcessState != nil {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
		}
	})
	return process
}

func stopDaemonAcceptanceProcess(t *testing.T, process *daemonAcceptanceProcess) {
	t.Helper()
	if process == nil || process.command.ProcessState != nil {
		return
	}
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	select {
	case err := <-process.done:
		if err != nil {
			log, _ := os.ReadFile(process.logPath)
			t.Fatalf("neondolld stopped with %v:\n%s", err, log)
		}
	case <-time.After(8 * time.Second):
		_ = process.command.Process.Kill()
		t.Fatal("neondolld did not stop")
	}
}

func runNeondollctl(t *testing.T, binary, socket string, arguments ...string) {
	t.Helper()
	args := append([]string{"-socket", socket}, arguments...)
	command := exec.Command(binary, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("neondollctl %v: %v\n%s", arguments, err, output)
	}
}

func executeDaemonAcceptance(t *testing.T, socket, bodyID, message string) corebody.ExecutionResult {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	requestValue := corebody.ExecutionRequest{
		ExecutionID: "daemon-" + message, Doll: "core4-acceptance", Body: corebody.BodyID(bodyID),
		Capability: "test.echo", Operation: "echo", Arguments: map[string]any{"message": message},
	}
	encoded, err := json.Marshal(requestValue)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://unix/execute", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
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

func assertDaemonEcho(t *testing.T, socket, bodyID, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := executeDaemonAcceptance(t, socket, bodyID, message)
		if result.Status == corebody.StatusSuccess {
			var output map[string]any
			if err := json.Unmarshal([]byte(result.Output), &output); err != nil || output["echo"] != message {
				t.Fatalf("daemon echo output = %q, %v", result.Output, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon echo did not become available: %#v", result)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func awaitDaemonExecutionStatus(t *testing.T, socket, bodyID string, status corebody.ExecutionStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := executeDaemonAcceptance(t, socket, bodyID, "status")
		if result.Status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon execution status = %q, want %q", result.Status, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertUnauthenticatedAdvertisementRejected(t *testing.T, websocketURL, bodyID string) {
	t.Helper()
	connection, _, err := websocket.DefaultDialer.Dial(websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	advertisement := capabilities.NewAdvertisement(bodyID, []capabilities.Capability{{ID: "test.echo", Operations: []string{"echo"}, Available: true}})
	if err := connection.WriteJSON(advertisement); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("unauthenticated capability advertisement did not close the connection")
	}
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for Unix socket %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
