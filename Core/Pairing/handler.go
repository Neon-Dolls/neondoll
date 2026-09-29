package pairing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── Handler ──────────────────────────────────────────────────────────────────

// Handler wraps a PairingService into an http.Handler for the /v1/pair
// endpoint. It handles request parsing, validation, and response writing.
type Handler struct {
	svc *PairingService
	log *slog.Logger
}

// NewHandler creates an HTTP handler for the pairing endpoint.
func NewHandler(svc *PairingService, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{svc: svc, log: log}
}

// HandlePair processes POST /v1/pair requests.
func (h *Handler) HandlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound the request body to 64 KB to prevent abuse.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

	var req dollnetwork.PairRequest
	if err := parseJSON(r, &req); err != nil {
		// Malformed JSON, trailing garbage, read errors, etc.
		h.log.Warn("pair request parse error", "error", err)
		writeJSON(w, http.StatusBadRequest, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("invalid request: %v", err),
			Reason:   "malformed_request",
			Consumed: false,
		})
		return
	}

	resp, errResp := h.svc.HandlePairing(r.Context(), &req)
	if errResp != nil {
		// Map protocol errors to HTTP status codes.
		status := http.StatusForbidden
		switch errResp.Reason {
		case "unsupported_version", "malformed_request", "missing_body_id", "invalid_wg_public_key":
			status = http.StatusBadRequest
		case "body_already_known":
			status = http.StatusConflict
		case "invalid_invitation", "invitation_already_consumed":
			status = http.StatusUnauthorized
		case "authorization_denied":
			status = http.StatusForbidden
		case "persistence_failed", "activation_failed", "key_assignment_failed", "internal_error":
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, errResp)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// parseJSON decodes a JSON request body, rejecting trailing garbage.
func parseJSON(r *http.Request, dest any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("json decode: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("trailing garbage after JSON body")
	}
	return nil
}

// writeJSON serialises data as JSON to the response writer.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Nothing we can do at this point — log and move on.
		slog.Warn("failed to encode JSON response", "error", err)
	}
}

// RegisterWithServer wires the pairing handler into an API server at /v1/pair.
// This is the intended integration point for production setup.
func RegisterWithServer(srv interface {
	RegisterHandler(pattern string, handler http.HandlerFunc)
}, svc *PairingService, log *slog.Logger) {
	h := NewHandler(svc, log)
	srv.RegisterHandler("/v1/pair", h.HandlePair)
}
