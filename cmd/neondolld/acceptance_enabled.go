//go:build acceptance

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	body "github.com/Neon-Dolls/neondoll/Core/Body"
)

// acceptanceAuthority is compiled only into the acceptance-test daemon. It
// authorizes one harmless capability and defaults every other request to deny.
type acceptanceAuthority struct{}

func (acceptanceAuthority) Evaluate(request body.AuthorityRequest) body.AuthorityResult {
	if request.Doll == "core4-acceptance" && request.Capability == "test.echo" && request.Operation == "echo" {
		return body.AuthorityResult{Decision: body.AuthorityAllow}
	}
	return body.AuthorityResult{Decision: body.AuthorityDeny, Reason: "acceptance_default_deny"}
}

// startAcceptanceControl exposes Guard.Execute only in binaries explicitly
// built with -tags=acceptance and only over an owner-local 0600 Unix socket.
// Normal neondolld binaries compile acceptance_disabled.go instead.
func startAcceptanceControl(registry *body.Registry) (func(), error) {
	path := os.Getenv("NEONDOLL_ACCEPTANCE_SOCKET")
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	guard := body.NewGuard(registry, acceptanceAuthority{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /execute", func(writer http.ResponseWriter, request *http.Request) {
		var value body.ExecutionRequest
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(guard.ExecuteContext(request.Context(), value))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = os.Remove(path)
	}, nil
}
