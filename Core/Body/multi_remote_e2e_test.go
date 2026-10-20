package body

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

const m8AuthHeader = "X-Neondoll-M8-Fixture"

var m8BodyIDs = []BodyID{"remote::m8-a", "remote::m8-b", "remote::m8-c"}

type m8EventHandler struct{}

func (m8EventHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "ack")
	return &response, nil
}

type m8BodyControl struct {
	bodyID BodyID

	permissionChecks atomic.Int32
	invocations      atomic.Int32
	started          chan string
	cancelled        chan string

	mu       sync.Mutex
	releases map[string]chan struct{}
}

func newM8BodyControl(bodyID BodyID) *m8BodyControl {
	return &m8BodyControl{
		bodyID: bodyID, started: make(chan string, 16), cancelled: make(chan string, 16),
		releases: make(map[string]chan struct{}),
	}
}

func (c *m8BodyControl) block(token string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	release := make(chan struct{})
	c.releases[token] = release
	return release
}

func (c *m8BodyControl) executor(ctx context.Context, _, operation string, arguments map[string]any) (map[string]any, error) {
	c.invocations.Add(1)
	if operation == "block" {
		token, _ := arguments["token"].(string)
		c.mu.Lock()
		release := c.releases[token]
		c.mu.Unlock()
		if release == nil {
			return nil, execution.ErrInvalid
		}
		c.started <- token
		select {
		case <-release:
		case <-ctx.Done():
			c.cancelled <- token
			return nil, ctx.Err()
		}
	}
	return map[string]any{"body": string(c.bodyID), "value": arguments["value"]}, nil
}

type m8ConnectedBody struct {
	client   *ws.Client
	done     chan error
	received chan events.Event
}

func (b *m8ConnectedBody) stop() error {
	_ = b.client.Close()
	select {
	case <-b.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("Body client did not stop")
	}
}

type m8Harness struct {
	t        *testing.T
	server   *ws.Server
	url      string
	registry *Registry
	guard    *Guard
	controls map[BodyID]*m8BodyControl
	clients  map[BodyID]*m8ConnectedBody
}

func newM8Harness(t *testing.T) *m8Harness {
	t.Helper()
	tokens := map[string]string{
		"token-a": string(m8BodyIDs[0]), "token-b": string(m8BodyIDs[1]), "token-c": string(m8BodyIDs[2]),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := ws.New(ws.Config{AuthenticateExecution: func(request *http.Request) (string, error) {
		bodyID := tokens[request.Header.Get(m8AuthHeader)]
		if bodyID == "" {
			return "", errors.New("invalid M8 trusted fixture credential")
		}
		return bodyID, nil
	}}, remoteTestLogger{}, m8EventHandler{})
	server.SetListener(listener)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(context.Background()) }()

	registry := NewRegistry()
	harness := &m8Harness{
		t: t, server: server, url: "ws://" + listener.Addr().String() + "/ws", registry: registry,
		controls: make(map[BodyID]*m8BodyControl), clients: make(map[BodyID]*m8ConnectedBody),
	}
	for i, bodyID := range m8BodyIDs {
		control := newM8BodyControl(bodyID)
		harness.controls[bodyID] = control
		remote, err := NewRemote(bodyID, string(bodyID), []Capability{{
			ID: "m8.echo", Operations: []string{"run", "block", "local-denied", "core-denied"}, Available: true,
		}}, server)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Register(remote); err != nil {
			t.Fatal(err)
		}
		harness.connect(bodyID, "token-"+string(rune('a'+i)), i == 1)
	}

	rules := make([]AuthorityRule, 0, len(m8BodyIDs)*4)
	for _, bodyID := range m8BodyIDs {
		for _, operation := range []string{"run", "block", "local-denied"} {
			rules = append(rules, AuthorityRule{
				Doll: "doll-m8", Body: bodyID, Capability: "m8.echo", Operation: operation, Decision: AuthorityAllow,
			})
		}
		rules = append(rules, AuthorityRule{
			Doll: "doll-m8", Body: bodyID, Capability: "m8.echo", Operation: "core-denied", Decision: AuthorityDeny, Reason: "m8_core_denied",
		})
	}
	harness.guard = NewGuard(registry, NewRuleEvaluator(rules...))

	t.Cleanup(func() {
		for bodyID, body := range harness.clients {
			if err := body.stop(); err != nil {
				t.Errorf("Body %s shutdown: %v", bodyID, err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("M8 server shutdown: %v", err)
		}
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("M8 server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("M8 server did not stop")
		}
	})
	return harness
}

func (h *m8Harness) connect(bodyID BodyID, token string, saturated bool) *m8ConnectedBody {
	h.t.Helper()
	header := http.Header{m8AuthHeader: []string{token}}
	conn, _, err := websocket.DefaultDialer.Dial(h.url, header)
	if err != nil {
		h.t.Fatal(err)
	}
	control := h.controls[bodyID]
	dispatcher, err := execution.NewDispatcher(string(bodyID), execution.LocalPermissionFunc(
		func(_ context.Context, request execution.Request) bool {
			control.permissionChecks.Add(1)
			return !(bodyID == m8BodyIDs[1] && request.Payload.Operation == "local-denied")
		},
	), execution.CapabilityExecutorFunc(control.executor))
	if err != nil {
		h.t.Fatal(err)
	}
	received := make(chan events.Event, 16)
	limit := 4
	if saturated {
		limit = 1
	}
	client, err := ws.NewClient(conn, ws.ClientConfig{
		AuthenticatedExecutionHandler: dispatcher,
		MaxConcurrentExecutions:       limit,
		EventReceiver: ws.EventReceiverFunc(func(_ context.Context, event *events.Event) error {
			received <- *event
			return nil
		}),
	}, remoteTestLogger{})
	if err != nil {
		h.t.Fatal(err)
	}
	connected := &m8ConnectedBody{client: client, done: make(chan error, 1), received: received}
	go func() { connected.done <- client.Run(context.Background()) }()
	h.clients[bodyID] = connected
	deadline := time.Now().Add(time.Second)
	for h.server.ConnCount() < len(h.clients) {
		if time.Now().After(deadline) {
			h.t.Fatal("M8 Body connection was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	return connected
}

func m8Request(bodyID BodyID, executionID, operation string, arguments map[string]any) ExecutionRequest {
	return ExecutionRequest{
		ExecutionID: executionID, Doll: "doll-m8", Body: bodyID,
		Capability: "m8.echo", Operation: operation, Arguments: arguments,
	}
}

func requireM8Success(t *testing.T, result ExecutionResult, executionID string, bodyID BodyID) {
	t.Helper()
	if result.Status != StatusSuccess || result.ExecutionID != executionID {
		t.Fatalf("execution result = %#v", result)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["body"] != string(bodyID) {
		t.Fatalf("result attributed to %v, want %s", output["body"], bodyID)
	}
}

func TestM8MultiBodyConcurrentExecutionAndIsolation(t *testing.T) {
	h := newM8Harness(t)
	if h.registry.Count() != 4 { // mandatory Local Body plus three remotes
		t.Fatalf("registry count = %d, want 4", h.registry.Count())
	}

	// All Bodies intentionally reuse the same execution ID. Doll Link message
	// correlation and authenticated session identity must keep them isolated.
	type outcome struct {
		bodyID BodyID
		result ExecutionResult
	}
	outcomes := make(chan outcome, len(m8BodyIDs))
	for _, bodyID := range m8BodyIDs {
		go func(bodyID BodyID) {
			result := h.guard.ExecuteContext(context.Background(), m8Request(bodyID, "overlapping-id", "run", map[string]any{"value": string(bodyID)}))
			outcomes <- outcome{bodyID: bodyID, result: result}
		}(bodyID)
	}
	for range m8BodyIDs {
		outcome := <-outcomes
		requireM8Success(t, outcome.result, "overlapping-id", outcome.bodyID)
	}

	// Core authority and Body-local permission are distinct and independently
	// scoped. Neither denial invokes the target capability.
	coreDeniedBody := m8BodyIDs[0]
	coreControl := h.controls[coreDeniedBody]
	checksBefore := coreControl.permissionChecks.Load()
	invocationsBefore := coreControl.invocations.Load()
	coreDenied := h.guard.Execute(m8Request(coreDeniedBody, "core-denied", "core-denied", nil))
	if coreDenied.Status != StatusDenied || coreControl.permissionChecks.Load() != checksBefore || coreControl.invocations.Load() != invocationsBefore {
		t.Fatalf("Core denial crossed remote boundary: result=%#v checks=%d invocations=%d", coreDenied, coreControl.permissionChecks.Load(), coreControl.invocations.Load())
	}
	localControl := h.controls[m8BodyIDs[1]]
	invocationsBefore = localControl.invocations.Load()
	localDenied := h.guard.Execute(m8Request(m8BodyIDs[1], "local-denied", "local-denied", nil))
	if localDenied.Status != StatusDenied || localControl.invocations.Load() != invocationsBefore {
		t.Fatalf("Body-local denial invoked capability: result=%#v invocations=%d", localDenied, localControl.invocations.Load())
	}

	// Saturating Body B cannot queue work indefinitely or interfere with A.
	releaseSaturation := localControl.block("saturation")
	firstB := make(chan ExecutionResult, 1)
	go func() {
		firstB <- h.guard.Execute(m8Request(m8BodyIDs[1], "sat-first", "block", map[string]any{"token": "saturation"}))
	}()
	if token := <-localControl.started; token != "saturation" {
		t.Fatalf("started token = %q", token)
	}
	saturated := h.guard.Execute(m8Request(m8BodyIDs[1], "sat-second", "run", nil))
	if saturated.Status != StatusUnavailable || saturated.ExecutionID != "sat-second" {
		t.Fatalf("saturation result = %#v", saturated)
	}
	unaffected := h.guard.Execute(m8Request(m8BodyIDs[0], "a-unaffected", "run", nil))
	requireM8Success(t, unaffected, "a-unaffected", m8BodyIDs[0])
	close(releaseSaturation)
	requireM8Success(t, <-firstB, "sat-first", m8BodyIDs[1])

	// Cancellation and timeout remove Core waiters without corrupting later
	// correlation. Body Protocol v1 has no cancellation message, so release the
	// Body work explicitly after the Core waiter terminates.
	for _, test := range []struct {
		name       string
		bodyID     BodyID
		cancelWith func(context.CancelFunc)
		status     ExecutionStatus
	}{
		{"cancel", m8BodyIDs[0], func(cancel context.CancelFunc) { cancel() }, StatusCancelled},
		{"timeout", m8BodyIDs[2], func(context.CancelFunc) {}, StatusExpired},
	} {
		control := h.controls[test.bodyID]
		release := control.block(test.name)
		var ctx context.Context
		var cancel context.CancelFunc
		if test.name == "timeout" {
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
		} else {
			ctx, cancel = context.WithCancel(context.Background())
		}
		done := make(chan ExecutionResult, 1)
		go func() {
			done <- h.guard.ExecuteContext(ctx, m8Request(test.bodyID, test.name, "block", map[string]any{"token": test.name}))
		}()
		if token := <-control.started; token != test.name {
			t.Fatalf("started token = %q", token)
		}
		test.cancelWith(cancel)
		result := <-done
		cancel()
		if result.Status != test.status || result.ExecutionID != test.name {
			t.Fatalf("%s result = %#v", test.name, result)
		}
		close(release)
		requireM8Success(t, h.guard.Execute(m8Request(test.bodyID, test.name+"-after", "run", nil)), test.name+"-after", test.bodyID)
	}

	// Disconnect B during execution. Its waiter and worker terminate while A
	// and C continue, then a fresh connection authenticated as B replaces it.
	releaseDisconnect := localControl.block("disconnect")
	pendingB := make(chan ExecutionResult, 1)
	go func() {
		pendingB <- h.guard.Execute(m8Request(m8BodyIDs[1], "disconnect", "block", map[string]any{"token": "disconnect"}))
	}()
	if token := <-localControl.started; token != "disconnect" {
		t.Fatalf("started token = %q", token)
	}
	peerResults := make(map[BodyID]chan ExecutionResult, 2)
	peerReleases := make(map[BodyID]chan struct{}, 2)
	for _, bodyID := range []BodyID{m8BodyIDs[0], m8BodyIDs[2]} {
		control := h.controls[bodyID]
		token := "in-flight-" + string(bodyID)
		peerReleases[bodyID] = control.block(token)
		result := make(chan ExecutionResult, 1)
		peerResults[bodyID] = result
		go func(bodyID BodyID, token string, result chan<- ExecutionResult) {
			result <- h.guard.Execute(m8Request(bodyID, "during-b-reconnect", "block", map[string]any{"token": token}))
		}(bodyID, token, result)
		if started := <-control.started; started != token {
			t.Fatalf("Body %s started token = %q", bodyID, started)
		}
	}
	oldB := h.clients[m8BodyIDs[1]]
	_ = oldB.client.Close()
	select {
	case result := <-pendingB:
		if result.Status != StatusFailed || result.ExecutionID != "disconnect" {
			t.Fatalf("disconnect result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not release Body B waiter")
	}
	select {
	case token := <-localControl.cancelled:
		if token != "disconnect" {
			t.Fatalf("cancelled token = %q", token)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel Body B work")
	}
	close(releaseDisconnect)
	delete(h.clients, m8BodyIDs[1])
	if err := oldB.stop(); err != nil {
		t.Fatal(err)
	}
	h.connect(m8BodyIDs[1], "token-b", true)
	requireM8Success(t, h.guard.Execute(m8Request(m8BodyIDs[1], "b-reconnected", "run", nil)), "b-reconnected", m8BodyIDs[1])
	for _, bodyID := range []BodyID{m8BodyIDs[0], m8BodyIDs[2]} {
		close(peerReleases[bodyID])
		requireM8Success(t, <-peerResults[bodyID], "during-b-reconnect", bodyID)
	}

	// Existing Event/Action traffic remains live on every connection.
	for _, bodyID := range m8BodyIDs {
		event := events.NewDollMessage("event-"+string(bodyID), "doll-m8", "hello")
		if err := h.clients[bodyID].client.SendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-h.clients[bodyID].received:
			if response.CorrelationID != event.ID {
				t.Fatalf("event correlation = %q, want %q", response.CorrelationID, event.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("Body %s did not receive Event response", bodyID)
		}
	}
	if err := h.server.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage, Payload: actions.SendMessagePayload{Text: "broadcast"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, bodyID := range m8BodyIDs {
		select {
		case event := <-h.clients[bodyID].received:
			if event.Type != events.TypeMessage {
				t.Fatalf("Body %s action type = %q", bodyID, event.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("Body %s did not receive Action", bodyID)
		}
	}

	localGuard := NewGuard(h.registry, NewRuleEvaluator(AuthorityRule{
		Doll: "doll-m8", Body: LocalBodyID, Capability: "runtime.info", Operation: "read", Decision: AuthorityAllow,
	}))
	local := localGuard.Execute(ExecutionRequest{
		ExecutionID: "m8-local", Doll: "doll-m8", Body: LocalBodyID, Capability: "runtime.info", Operation: "read",
	})
	if local.Status != StatusSuccess || local.ExecutionID != "m8-local" {
		t.Fatalf("Local Body result = %#v", local)
	}
}
