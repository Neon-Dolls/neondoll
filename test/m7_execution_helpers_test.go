//go:build e2e

package integration

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.zx2c4.com/wireguard/tun/netstack"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
)

const (
	m7ExecutionPort  = 8081
	m7ExecutionToken = "trusted-m7-execution-fixture"
)

type m7ExecutionLink struct {
	server     *ws.Server
	client     *ws.Client
	serverDone chan error
	clientDone chan error
	stopOnce   sync.Once
	stopErr    error
}

func startM7ExecutionLink(t *testing.T, coreNet, bodyNet *netstack.Net, coreAddress netip.Addr, bodyID string, dispatcher *execution.Dispatcher, log *slog.Logger) *m7ExecutionLink {
	t.Helper()
	listener, err := coreNet.ListenTCP(&net.TCPAddr{IP: coreAddress.AsSlice(), Port: m7ExecutionPort})
	if err != nil {
		t.Fatalf("Doll Link listen: %v", err)
	}
	server := ws.New(ws.Config{AuthenticateExecution: func(request *http.Request) (string, error) {
		if request.Header.Get("Authorization") != "Bearer "+m7ExecutionToken {
			return "", errors.New("invalid trusted M7 fixture credential")
		}
		return bodyID, nil
	}}, &wsLogger{inner: log}, nil)
	server.SetListener(listener)
	link := &m7ExecutionLink{
		server: server, serverDone: make(chan error, 1), clientDone: make(chan error, 1),
	}
	go func() { link.serverDone <- server.Start(context.Background()) }()

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDialContext:   bodyNet.DialContext,
	}
	header := http.Header{"Authorization": []string{"Bearer " + m7ExecutionToken}}
	conn, _, err := dialer.Dial("ws://"+net.JoinHostPort(coreAddress.String(), "8081")+"/ws", header)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("Body Doll Link dial: %v", err)
	}
	client, err := ws.NewClient(conn, ws.ClientConfig{AuthenticatedExecutionHandler: dispatcher}, &wsLogger{inner: log})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("Body Doll Link client: %v", err)
	}
	link.client = client
	go func() { link.clientDone <- client.Run(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for server.ConnCount() != 1 {
		if time.Now().After(deadline) {
			link.stop(time.Second)
			t.Fatal("authenticated Body Doll Link did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	return link
}

func (l *m7ExecutionLink) stop(timeout time.Duration) error {
	l.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := l.server.Shutdown(ctx); err != nil {
			l.stopErr = err
		}
		_ = l.client.Close()
		select {
		case <-l.clientDone:
		case <-ctx.Done():
			if l.stopErr == nil {
				l.stopErr = errors.New("Body Doll Link client did not stop")
			}
		}
		select {
		case err := <-l.serverDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && l.stopErr == nil {
				l.stopErr = err
			}
		case <-ctx.Done():
			if l.stopErr == nil {
				l.stopErr = errors.New("Core Doll Link server did not stop")
			}
		}
	})
	return l.stopErr
}

func newM7RemoteGuard(t *testing.T, bodyID string, transport corebody.RemoteExecutionTransport) *corebody.Guard {
	t.Helper()
	remote, err := corebody.NewRemote(corebody.BodyID(bodyID), "M7 migrated Body", []corebody.Capability{{
		ID: "test.echo", Operations: []string{"run", "block"}, Available: true,
	}}, transport)
	if err != nil {
		t.Fatal(err)
	}
	registry := corebody.NewRegistry()
	if err := registry.Register(remote); err != nil {
		t.Fatal(err)
	}
	return corebody.NewGuard(registry, corebody.NewRuleEvaluator(
		corebody.AuthorityRule{Doll: "doll-m7", Body: corebody.BodyID(bodyID), Capability: "test.echo", Operation: "run", Decision: corebody.AuthorityAllow},
		corebody.AuthorityRule{Doll: "doll-m7", Body: corebody.BodyID(bodyID), Capability: "test.echo", Operation: "block", Decision: corebody.AuthorityAllow},
	))
}

func m7ExecutionRequest(bodyID, executionID, operation string) corebody.ExecutionRequest {
	return corebody.ExecutionRequest{
		ExecutionID: executionID,
		Doll:        "doll-m7",
		Body:        corebody.BodyID(bodyID),
		Capability:  "test.echo",
		Operation:   operation,
		Arguments:   map[string]any{"message": "migration-ok"},
	}
}
