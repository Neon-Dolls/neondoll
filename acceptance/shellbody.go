package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	body "github.com/Neon-Dolls/neondoll/Core/Body"
	enrollment "github.com/Neon-Dolls/neondoll/Core/Enrollment"
	invitation "github.com/Neon-Dolls/neondoll/Core/Invitation"
	network "github.com/Neon-Dolls/neondoll/Core/Network"
	ownercontrol "github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	persistence "github.com/Neon-Dolls/neondoll/Core/Persistence"
	websocket "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

const ShellBodyRevision = "10b2d510d9a5887800c8e6f72c2ef05530bf6c46"

const (
	ScenarioShellBodyAuthenticated = "shellbody-authenticated-connection"
	ScenarioShellBodyExecution     = "shellbody-remote-execution"
	ScenarioShellBodyRecovery      = "shellbody-connection-recovery"
)

type ShellBodyDriverConfig struct {
	SourceDir string
	Binary    string
	Revision  string
	Timeout   time.Duration
}

// ShellBodyDriver builds or uses an independently compiled Shell Body and
// owns every process it starts. It never falls back to an in-process Body.
type ShellBodyDriver struct {
	config     ShellBodyDriverConfig
	once       sync.Once
	binary     string
	buildDir   string
	prepareErr error
}

func NewShellBodyDriver(config ShellBodyDriverConfig) *ShellBodyDriver {
	if config.Revision == "" {
		config.Revision = ShellBodyRevision
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	return &ShellBodyDriver{config: config}
}

func (d *ShellBodyDriver) Revision() string { return d.config.Revision }

func (d *ShellBodyDriver) Prepare(ctx context.Context) error {
	d.once.Do(func() { d.prepareErr = d.prepare(ctx) })
	return d.prepareErr
}

func (d *ShellBodyDriver) prepare(ctx context.Context) error {
	if d.config.Binary != "" {
		info, err := os.Stat(d.config.Binary)
		if err != nil {
			return fmt.Errorf("shellbody binary unavailable: %w", err)
		}
		if info.Mode()&0o111 == 0 {
			return fmt.Errorf("shellbody binary is not executable: %s", d.config.Binary)
		}
		d.binary = d.config.Binary
		return nil
	}
	if d.config.SourceDir == "" {
		return errors.New("shellbody source or binary is required; no simulated fallback is permitted")
	}
	revision, err := commandOutput(ctx, d.config.SourceDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("shellbody revision: %w", err)
	}
	if strings.TrimSpace(revision) != d.config.Revision {
		return fmt.Errorf("shellbody checkout revision %s, want pinned %s", strings.TrimSpace(revision), d.config.Revision)
	}
	d.buildDir, err = os.MkdirTemp("", "neondoll-shellbody-build-")
	if err != nil {
		return err
	}
	d.binary = filepath.Join(d.buildDir, "neondoll-shellbody")
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(buildCtx, "go", "build", "-trimpath", "-ldflags", "-X main.version="+d.config.Revision, "-o", d.binary, "./cmd/neondoll-shellbody")
	command.Dir = d.config.SourceDir
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		return fmt.Errorf("build shellbody: %w: %s", buildErr, tail(string(output), 4096))
	}
	return nil
}

func (d *ShellBodyDriver) Close() error {
	if d.buildDir != "" {
		return os.RemoveAll(d.buildDir)
	}
	return nil
}

func Core5ExternalProfile(driver *ShellBodyDriver) Profile {
	profile := Core5Profile()
	realParticipants := []Participant{
		{Kind: "core", Mode: "real-in-process", Revision: CoreRevision(), Boundary: "production in-process Enrollment, authenticated Doll Link WebSocket, Registry, and Guard"},
		{Kind: "body", Mode: "real", Revision: driver.Revision(), Boundary: "independent neondoll-shellbody OS process over production Doll Link"},
	}
	profile.Scenarios = append(profile.Scenarios,
		Scenario{ID: ScenarioShellBodyAuthenticated, Description: "real Shell Body enrolls, authenticates, and advertises capabilities", Participants: realParticipants, Run: driver.runAuthenticated},
		Scenario{ID: ScenarioShellBodyExecution, Description: "authorized process capability executes on the real Shell Body", Participants: realParticipants, Run: driver.runExecution},
		Scenario{ID: ScenarioShellBodyRecovery, Description: "real Shell Body disconnects and returns with its persistent identity", Participants: realParticipants, Run: driver.runRecovery},
	)
	return profile
}

type shellCore struct {
	ctx         context.Context
	cancel      context.CancelFunc
	store       persistence.Store
	server      *websocket.Server
	registry    *body.Registry
	guard       *body.Guard
	invitations *invitation.Service
	owner       *ownercontrol.Service
	baseURL     string
	wsURL       string
}

type shellAuthority struct{}

func (shellAuthority) Evaluate(request body.AuthorityRequest) body.AuthorityResult {
	if request.Doll == "acceptance-doll" && request.Capability == "process" && request.Operation == "run" {
		return body.AuthorityResult{Decision: body.AuthorityAllow}
	}
	return body.AuthorityResult{Decision: body.AuthorityDeny, Reason: "acceptance_not_authorized", Scope: body.AuthorityScopeBody}
}

func startShellCore(parent context.Context, directory string) (*shellCore, error) {
	ctx, cancel := context.WithCancel(parent)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		cancel()
		return nil, err
	}
	store, err := persistence.NewStore(filepath.Join(directory, "core.db"))
	if err != nil {
		cancel()
		return nil, err
	}
	nw, err := network.NewNetwork("acceptance-shellbody-network")
	if err != nil {
		cancel()
		store.Close()
		return nil, err
	}
	if err = store.(network.NetworkStore).SaveNetwork(ctx, nw); err != nil {
		cancel()
		store.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		store.Close()
		return nil, err
	}
	registry := body.NewRegistry()
	server := websocket.New(websocket.Config{}, logger.New(logger.ErrorLevel, io.Discard), nil)
	invites := invitation.NewService(store.(invitation.Store), invitation.RealClock{})
	boundary := ownercontrol.NewBoundary()
	enrollments, err := enrollment.NewService(ctx, store.(enrollment.Store), nw, invites, boundary, nil, enrollment.DefaultConfig())
	if err != nil {
		listener.Close()
		cancel()
		store.Close()
		return nil, err
	}
	enrollment.RegisterBodyEndpoints(server, enrollments)
	server.SetBodyAuthenticator(enrollments)
	registrar, err := body.NewRemoteRegistrar(registry, store.(network.NetworkStore), server)
	if err != nil {
		listener.Close()
		cancel()
		store.Close()
		return nil, err
	}
	server.SetExecutionSessionLifecycle(registrar)
	enrollments.SetRevocationLifecycle(server, nil)
	server.SetListener(listener)
	core := &shellCore{ctx: ctx, cancel: cancel, store: store, server: server, registry: registry, guard: body.NewGuard(registry, shellAuthority{}), invitations: invites, owner: ownercontrol.NewService(enrollments, invites, boundary), baseURL: "http://" + listener.Addr().String(), wsURL: "ws://" + listener.Addr().String() + "/ws"}
	go func() { _ = server.Start(ctx) }()
	return core, nil
}

func (c *shellCore) Close() {
	c.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.server.Shutdown(ctx)
	_ = c.store.Close()
}

func (c *shellCore) invitationFile(ctx context.Context, directory string) (string, error) {
	created, err := c.owner.CreateInvitation(ctx, time.Minute, []string{"process"}, dollnetwork.Endpoints{{URL: c.baseURL}})
	if err != nil {
		return "", err
	}
	document := map[string]any{"version": 1, "invitation_id": created.ID, "invitation_secret": created.Secret, "expires_at": created.ExpiresAt.UTC().Format(time.RFC3339), "bootstrap_endpoints": created.BootstrapEndpoints}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "invitation.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

type shellSession struct {
	driver    *ShellBodyDriver
	core      *shellCore
	directory string
	stateDir  string
	bodyID    string
	process   *managedProcess
}

func (d *ShellBodyDriver) connect(ctx context.Context) (*shellSession, error) {
	if err := d.Prepare(ctx); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "neondoll-shellbody-acceptance-")
	if err != nil {
		return nil, err
	}
	session := &shellSession{driver: d, directory: directory, stateDir: filepath.Join(directory, "body")}
	fail := func(err error) (*shellSession, error) { session.Close(); return nil, err }
	core, err := startShellCore(ctx, filepath.Join(directory, "core"))
	if err != nil {
		return fail(err)
	}
	session.core = core
	if _, err = d.run(ctx, "--init", "--state-dir", session.stateDir, "--build", d.config.Revision); err != nil {
		return fail(fmt.Errorf("initialize shellbody: %w", err))
	}
	invitationFile, err := core.invitationFile(ctx, directory)
	if err != nil {
		return fail(err)
	}
	output, err := d.run(ctx, "--enroll", invitationFile, "--core-url", core.baseURL, "--state-dir", session.stateDir)
	if err != nil {
		return fail(fmt.Errorf("enroll shellbody: %w", err))
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "enrolled body_id=") {
			session.bodyID = strings.TrimPrefix(line, "enrolled body_id=")
		}
	}
	if session.bodyID == "" {
		return fail(fmt.Errorf("enrollment produced no Body ID: %s", tail(output, 2048)))
	}
	if err = session.startServe(ctx); err != nil {
		return fail(err)
	}
	if err = session.waitAvailable(ctx); err != nil {
		return fail(err)
	}
	return session, nil
}

func (d *ShellBodyDriver) run(ctx context.Context, arguments ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, d.config.Timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, d.binary, arguments...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if runCtx.Err() != nil {
		return output.String(), fmt.Errorf("process timeout: %w; output: %s", runCtx.Err(), tail(output.String(), 4096))
	}
	if err != nil {
		return output.String(), fmt.Errorf("process exit: %w; output: %s", err, tail(output.String(), 4096))
	}
	return output.String(), nil
}

func (s *shellSession) startServe(ctx context.Context) error {
	process, err := startManagedProcess(ctx, s.driver.binary, "--serve", s.core.wsURL, "--state-dir", s.stateDir)
	if err != nil {
		return err
	}
	s.process = process
	return nil
}

func (s *shellSession) waitAvailable(ctx context.Context) error {
	deadline := time.Now().Add(s.driver.config.Timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.process != nil && s.process.Exited() {
			return fmt.Errorf("shellbody exited before readiness: %s", s.process.Diagnostics())
		}
		if capabilities, err := s.core.registry.GetCapabilities(body.BodyID(s.bodyID)); err == nil {
			for _, capability := range capabilities {
				if capability.ID == "process" && capability.Available {
					for _, operation := range capability.Operations {
						if operation == "run" {
							return nil
						}
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("capability registration timeout for Body %s; output: %s", s.bodyID, s.process.Diagnostics())
}

func (s *shellSession) Close() {
	if s.process != nil {
		_ = s.process.Stop(3 * time.Second)
		s.process = nil
	}
	if s.core != nil {
		s.core.Close()
		s.core = nil
	}
	if s.directory != "" {
		_ = os.RemoveAll(s.directory)
	}
}

func (s *shellSession) execute(ctx context.Context, id string) body.ExecutionResult {
	return s.core.guard.ExecuteContext(ctx, body.ExecutionRequest{ExecutionID: id, Doll: "acceptance-doll", Body: body.BodyID(s.bodyID), Capability: "process", Operation: "run", Arguments: map[string]any{"command": "printf", "args": []string{"shellbody-acceptance"}}})
}

func (d *ShellBodyDriver) runAuthenticated(ctx context.Context) error {
	session, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	registered, ok := session.core.registry.Get(body.BodyID(session.bodyID))
	if !ok || registered.Kind() != body.BodyKindRemote {
		return fmt.Errorf("authenticated Body absent from registry: %s", session.bodyID)
	}
	return nil
}

func (d *ShellBodyDriver) runExecution(ctx context.Context) error {
	session, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	result := session.execute(ctx, "shellbody-execution")
	if result.ExecutionID != "shellbody-execution" || result.Status != body.StatusSuccess || !strings.Contains(result.Output, "shellbody-acceptance") {
		return fmt.Errorf("remote execution result=%#v", result)
	}
	marker := filepath.Join(session.directory, "must-not-exist")
	deniedGuard := body.NewGuard(session.core.registry, body.NewRuleEvaluator())
	denied := deniedGuard.ExecuteContext(ctx, body.ExecutionRequest{ExecutionID: "shellbody-denied", Doll: "acceptance-doll", Body: body.BodyID(session.bodyID), Capability: "process", Operation: "run", Arguments: map[string]any{"command": "touch", "args": []string{marker}}})
	if denied.Status != body.StatusDenied {
		return fmt.Errorf("denied execution status=%s", denied.Status)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		return errors.New("denied action reached Shell Body")
	}
	return nil
}

func (d *ShellBodyDriver) runRecovery(ctx context.Context) error {
	session, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	bodyID := session.bodyID
	if err = session.process.Stop(3 * time.Second); err != nil {
		return err
	}
	session.process = nil
	deadline := time.Now().Add(d.config.Timeout)
	unavailable := false
	for time.Now().Before(deadline) {
		if err := session.core.registry.ResolveCapability(body.BodyID(bodyID), "process", "run"); errors.Is(err, body.ErrUnavailable) {
			unavailable = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !unavailable {
		return fmt.Errorf("Core did not mark disconnected Body %s unavailable", bodyID)
	}
	disconnected := session.execute(ctx, "shellbody-disconnected")
	if disconnected.Status != body.StatusUnavailable {
		return fmt.Errorf("disconnected execution status=%s, want unavailable", disconnected.Status)
	}
	if err = session.startServe(ctx); err != nil {
		return err
	}
	if err = session.waitAvailable(ctx); err != nil {
		return err
	}
	if session.bodyID != bodyID {
		return fmt.Errorf("Body identity changed: %s != %s", session.bodyID, bodyID)
	}
	result := session.execute(ctx, "shellbody-recovered")
	if result.Status != body.StatusSuccess || result.ExecutionID != "shellbody-recovered" {
		return fmt.Errorf("post-recovery execution=%#v", result)
	}
	return nil
}

type managedProcess struct {
	command *exec.Cmd
	output  *boundedBuffer
	done    chan error
	mu      sync.Mutex
	exited  bool
}

func startManagedProcess(ctx context.Context, binary string, arguments ...string) (*managedProcess, error) {
	command := exec.CommandContext(ctx, binary, arguments...)
	if runtime.GOOS != "windows" {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	output := &boundedBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start shellbody: %w", err)
	}
	process := &managedProcess{command: command, output: output, done: make(chan error, 1)}
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.exited = true
		process.mu.Unlock()
		process.done <- err
	}()
	return process, nil
}

func (p *managedProcess) Exited() bool        { p.mu.Lock(); defer p.mu.Unlock(); return p.exited }
func (p *managedProcess) Diagnostics() string { return tail(p.output.String(), 4096) }
func (p *managedProcess) Stop(timeout time.Duration) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	if p.Exited() {
		<-p.done
		return nil
	}
	_ = p.command.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
	}
	if runtime.GOOS != "windows" {
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
	} else {
		_ = p.command.Process.Kill()
	}
	<-p.done
	return nil
}

type boundedBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

func commandOutput(ctx context.Context, directory, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, tail(string(output), 2048))
	}
	return string(output), nil
}
func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
