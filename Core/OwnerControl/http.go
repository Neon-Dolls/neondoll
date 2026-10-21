package ownercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type approveRequest struct {
	Code        string   `json:"code"`
	Permissions []string `json:"permissions,omitempty"`
}
type denyRequest struct {
	Code string `json:"code"`
}
type invitationRequest struct {
	LifetimeSeconds int64                 `json:"lifetime_seconds,omitempty"`
	Permissions     []string              `json:"permissions,omitempty"`
	Endpoints       dollnetwork.Endpoints `json:"bootstrap_endpoints,omitempty"`
}
type revokeRequest struct {
	BodyID string `json:"body_id"`
}

type Server struct {
	path     string
	listener net.Listener
	http     *http.Server
}

func NewServer(path string, service *Service) (*Server, error) {
	if path == "" || service == nil {
		return nil, errors.New("owner control: socket path and service are required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("owner control: refusing to replace non-socket path")
		}
		if connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond); dialErr == nil {
			connection.Close()
			return nil, errors.New("owner control: administrative socket is already active")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	mux := http.NewServeMux()
	server := &Server{path: path, listener: listener}
	mux.HandleFunc("GET /v1/pending", func(w http.ResponseWriter, r *http.Request) {
		value, err := service.ListPending(r.Context())
		writeResult(w, value, err)
	})
	mux.HandleFunc("POST /v1/approve", func(w http.ResponseWriter, r *http.Request) {
		var request approveRequest
		if !decode(w, r, &request) {
			return
		}
		value, err := service.Approve(r.Context(), request.Code, request.Permissions)
		writeResult(w, value, err)
	})
	mux.HandleFunc("POST /v1/deny", func(w http.ResponseWriter, r *http.Request) {
		var request denyRequest
		if !decode(w, r, &request) {
			return
		}
		writeError(w, service.Deny(r.Context(), request.Code))
	})
	mux.HandleFunc("POST /v1/invitations", func(w http.ResponseWriter, r *http.Request) {
		var request invitationRequest
		if !decode(w, r, &request) {
			return
		}
		value, err := service.CreateInvitation(r.Context(), time.Duration(request.LifetimeSeconds)*time.Second, request.Permissions, request.Endpoints)
		writeResult(w, value, err)
	})
	mux.HandleFunc("GET /v1/bodies", func(w http.ResponseWriter, r *http.Request) {
		value, err := service.ListBodies(r.Context())
		writeResult(w, value, err)
	})
	mux.HandleFunc("POST /v1/revoke", func(w http.ResponseWriter, r *http.Request) {
		var request revokeRequest
		if !decode(w, r, &request) {
			return
		}
		writeError(w, service.Revoke(r.Context(), request.BodyID))
	})
	server.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return server, nil
}

func (s *Server) Serve() error { return s.http.Serve(s.listener) }

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	removeErr := os.Remove(s.path)
	if err != nil {
		return err
	}
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeResult[T any](w http.ResponseWriter, value T, err error) {
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAPIError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, enrollment.ErrUnauthorized):
		status = http.StatusForbidden
	case errors.Is(err, enrollment.ErrUnknownBody), errors.Is(err, enrollment.ErrUnknownRequest):
		status = http.StatusNotFound
	case errors.Is(err, enrollment.ErrExpired):
		status = http.StatusGone
	case errors.Is(err, enrollment.ErrDenied), errors.Is(err, enrollment.ErrReplayed):
		status = http.StatusConflict
	case errors.Is(err, enrollment.ErrRateLimited):
		status = http.StatusTooManyRequests
	}
	http.Error(w, http.StatusText(status), status)
}
