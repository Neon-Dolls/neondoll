package enrollment

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

const maxEnrollmentRequest = 64 << 10

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) CoreIdentity(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeEnrollmentJSON(w, http.StatusMethodNotAllowed, dollnetwork.EnrollmentErrorResponse{Version: 1, Error: dollnetwork.EnrollmentError{Code: "invalid_request", Message: "method not allowed"}})
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, h.service.PublicCoreIdentity())
}

func (h *Handler) Begin(w http.ResponseWriter, request *http.Request) {
	var body dollnetwork.EnrollmentBeginRequest
	if !h.decode(w, request, &body) {
		return
	}
	response, err := h.service.Begin(request.Context(), body)
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, response)
}

func (h *Handler) Redeem(w http.ResponseWriter, request *http.Request) {
	var body dollnetwork.EnrollmentRedeemRequest
	if !h.decode(w, request, &body) {
		return
	}
	receipt, err := h.service.Redeem(request.Context(), body)
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, receipt)
}

func (h *Handler) Result(w http.ResponseWriter, request *http.Request) {
	var body dollnetwork.EnrollmentResultRequest
	if !h.decode(w, request, &body) {
		return
	}
	response, err := h.service.Result(request.Context(), body)
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, response)
}

func (h *Handler) decode(w http.ResponseWriter, request *http.Request, destination any) bool {
	if request.Method != http.MethodPost {
		writeEnrollmentJSON(w, http.StatusMethodNotAllowed, dollnetwork.EnrollmentErrorResponse{Version: 1, Error: dollnetwork.EnrollmentError{Code: "invalid_request", Message: "method not allowed"}})
		return false
	}
	request.Body = http.MaxBytesReader(w, request.Body, maxEnrollmentRequest)
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(destination); err != nil {
		writeEnrollmentJSON(w, http.StatusBadRequest, dollnetwork.EnrollmentErrorResponse{Version: 1, Error: dollnetwork.EnrollmentError{Code: "invalid_request", Message: "malformed enrollment request"}})
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeEnrollmentJSON(w, http.StatusBadRequest, dollnetwork.EnrollmentErrorResponse{Version: 1, Error: dollnetwork.EnrollmentError{Code: "invalid_request", Message: "malformed enrollment request"}})
		return false
	}
	return true
}

func RegisterBodyEndpoints(server interface {
	RegisterHandler(string, http.HandlerFunc)
}, service *Service) {
	handler := NewHandler(service)
	server.RegisterHandler("/v1/core/identity", handler.CoreIdentity)
	server.RegisterHandler("/v1/body/enrollment/begin", handler.Begin)
	server.RegisterHandler("/v1/body/enrollment/redeem", handler.Redeem)
	server.RegisterHandler("/v1/body/enrollment/result", handler.Result)
}

func writeEnrollmentError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "unavailable"
	switch {
	case errors.Is(err, ErrUnsupportedVersion):
		status, code = http.StatusBadRequest, "unsupported_version"
	case errors.Is(err, ErrInvalidProof):
		status, code = http.StatusUnauthorized, "invalid_proof"
	case errors.Is(err, ErrCoreIdentity):
		status, code = http.StatusUnauthorized, "core_identity_mismatch"
	case errors.Is(err, ErrUnauthorized):
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrUnknownRequest):
		status, code = http.StatusNotFound, "unknown_request"
	case errors.Is(err, ErrExpired):
		status, code = http.StatusGone, "expired"
	case errors.Is(err, ErrDenied):
		status, code = http.StatusForbidden, "denied"
	case errors.Is(err, ErrReplayed):
		status, code = http.StatusConflict, "replayed"
	case errors.Is(err, ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, ErrRevoked):
		status, code = http.StatusForbidden, "revoked"
	}
	writeEnrollmentJSON(w, status, dollnetwork.EnrollmentErrorResponse{Version: 1, Error: dollnetwork.EnrollmentError{Code: code, Message: "enrollment request rejected"}})
}

func writeEnrollmentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
