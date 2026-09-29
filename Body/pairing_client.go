// Package body — reusable, Core-independent reference Body runtime.
//
// M2 — Body Pairs.
//
// This file implements the pipeline a fresh Body runs to consume a Core
// pairing invitation, pair through the canonical direct HTTP(S) bootstrap,
// validate Core's membership response, and persist the resulting Doll Network
// membership:
//
//  1. Consume & validate the invitation (version, id, secret, expiry,
//     advertised bootstrap endpoints).
//  2. Choose a supported bootstrap endpoint. As of M2 only direct HTTP(S)
//     bootstrap is implemented; a relay endpoint is representable but fails
//     explicitly as unsupported rather than silently behaving like direct.
//  3. Build the canonical DollNetwork.PairRequest from the persisted Body
//     identity + WG keypair, and submit it to Core over HTTP(S).
//  4. Treat Core's response as untrusted network input: validate the
//     protocol version, required membership fields, IPv6 address/prefixes,
//     and the Core WG public key (canonical DecodeWgPublicKey).
//  5. Commit the membership durably ONLY after the entire response validates
//     (atomic: no partial durable state). The invitation secret is never
//     persisted and never logged.
//
// The reusable pairing logic lives here, outside cmd/neondoll-body (which
// only parses inputs and invokes it).
package body

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// PairingHTTPTimeout is the bounded per-request timeout for a pairing
// handshake. Core responses to a pairing request are expected to be small and
// prompt; an unbounded wait is never acceptable.
const PairingHTTPTimeout = 30 * time.Second

// MaxPairingResponseBytes bounds the size of Core's pairing response body we
// are willing to read (1 MiB). Larger responses are rejected rather than read
// into memory, so a hostile/broken Core cannot exhaust the Body.
const MaxPairingResponseBytes = 1 * 1024 * 1024

// pairingEndpointPath is the canonical HTTP endpoint path Core serves the
// pairing flow at. The DollNetwork package owns the wire-protocol types but
// not the serving path; Core exposes it as `/v1/pair`.
const pairingEndpointPath = "/v1/pair"

// PairingError describes why a pairing attempt failed. It is the canonical
// structured error type for the Body pairing client (distinct from a raw
// transport error). `Cause` carries the underlying failure, if any.
type PairingError struct {
	Op    string
	Cause error
}

// Error returns a short description of the pairing failure.
func (e *PairingError) Error() string {
	if e.Cause != nil {
		return "body: pairing: " + e.Op + ": " + e.Cause.Error()
	}
	return "body: pairing: " + e.Op
}

// ErrPairingUnsupportedBootstrap is returned when the invitation offers no
// supported transport (e.g. only relay, which is not yet implemented).
var ErrPairingUnsupportedBootstrap error = &PairingError{Op: "no supported bootstrap endpoint"}

// LoadInvitation parses an invitation document and validates its version and
// required fields (id, secret, expiry parseable, at least one bootstrap
// endpoint). It does NOT verify expiry against the current time — callers pass
// a current-second value to PairWithInvitation, which checks it. Malformed
// input is returned as a PairingError.
func LoadInvitation(raw string) (*dollnetwork.Invitation, error) {
	var inv dollnetwork.Invitation
	if err := json.Unmarshal([]byte(raw), &inv); err != nil {
		return nil, &PairingError{Op: "parse invitation", Cause: err}
	}
	if err := validateInvitationShape(&inv); err != nil {
		return nil, &PairingError{Op: "validate invitation", Cause: err}
	}
	return &inv, nil
}

// validateInvitationShape checks the structural validity of an invitation
// without reference to the current time: version, id, secret, expiry syntax,
// and at least one advertised bootstrap endpoint.
func validateInvitationShape(inv *dollnetwork.Invitation) error {
	if inv == nil {
		return errors.New("nil invitation")
	}
	if inv.Version != dollnetwork.ProtocolVersion {
		return fmt.Errorf("unsupported invitation version %d", inv.Version)
	}
	if inv.InvitationID == "" {
		return errors.New("missing invitation id")
	}
	if inv.InvitationSecret == "" {
		return errors.New("missing invitation secret")
	}
	if _, err := time.Parse(time.RFC3339, inv.ExpiresAt); err != nil {
		return fmt.Errorf("invitation expires_at %q is not RFC3339: %v", inv.ExpiresAt, err)
	}
	if len(inv.BootstrapEndpoints) == 0 {
		return errors.New("invitation has no bootstrap endpoints")
	}
	return nil
}

// invitationExpired reports whether the invitation has expired as of `now`
// (a Unix timestamp, matching the convention used across the Body runtime).
func invitationExpired(inv *dollnetwork.Invitation, now int64) bool {
	exp, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	if err != nil {
		// validateInvitationShape already guaranteed parseability; treat any
		// surprise as expired so we never accept a malformed date.
		return true
	}
	return exp.Before(time.Unix(now, 0)) || exp.Equal(time.Unix(now, 0))
}

// ChooseBootstrapEndpoint selects a supported bootstrap endpoint from the
// invitation. As of M2 the Body implements direct HTTP(S) bootstrap; the
// canonical BootstrapEndpoint carries only a URL, and we distinguish transport
// by scheme — an http(s) URL is a direct bootstrap, anything else (e.g.
// relay://) is unsupported. If no direct endpoint exists we fail explicitly
// rather than treating relay like direct.
func ChooseBootstrapEndpoint(inv *dollnetwork.Invitation) (*dollnetwork.BootstrapEndpoint, error) {
	if inv == nil {
		return nil, errors.New("body: pairing: nil invitation")
	}
	for _, e := range inv.BootstrapEndpoints {
		u, err := url.Parse(e.URL)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return &e, nil
		}
	}
	return nil, ErrPairingUnsupportedBootstrap
}

// membershipFromResponse maps a validated PairResponse onto durable Body
// membership state. It carries exactly the fields the M2 milestone requires to
// persist: network_id, Body peer_id, Body overlay IPv6, Core peer_id, and Core
// WG public key. It deliberately does NOT carry transient endpoints or the
// invitation secret.
func membershipFromResponse(resp *dollnetwork.PairResponse) Membership {
	return Membership{
		NetworkID:    resp.NetworkID,
		Status:       MembershipActive,
		PeerID:       resp.BodyPeerID,
		BodyPeerID:   resp.BodyPeerID,
		BodyIPv6:     firstAddress(resp.BodyAddresses),
		CorePeerID:   resp.CorePeerID,
		CoreWGKeyB64: resp.CoreWGPublicKey,
	}
}

func firstAddress(addrs []string) string {
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// validatePairResponse treats Core's response as untrusted input and checks it
// structurally before anything is persisted: protocol version, required
// membership fields, at least one Body/Core overlay address, a parseable IPv6
// prefix, and a Core WG public key that decodes to the canonical 32 bytes.
func validatePairResponse(resp *dollnetwork.PairResponse) error {
	if resp == nil {
		return errors.New("nil pair response")
	}
	if resp.Version != dollnetwork.ProtocolVersion {
		return fmt.Errorf("unsupported response version %d", resp.Version)
	}
	if resp.NetworkID == "" {
		return errors.New("response missing network_id")
	}
	if resp.BodyPeerID == "" {
		return errors.New("response missing body_peer_id")
	}
	if resp.CorePeerID == "" {
		return errors.New("response missing core_peer_id")
	}
	if addr, ok := validOverlayAddress(resp.BodyAddresses); !ok {
		return fmt.Errorf("response has no valid body overlay address (got %q)", addr)
	}
	if _, ok := validOverlayAddress(resp.CoreAddresses); !ok {
		return errors.New("response has no valid core overlay address")
	}
	if _, err := dollnetwork.DecodeWgPublicKey(resp.CoreWGPublicKey); err != nil {
		return fmt.Errorf("response core wg public key invalid: %v", err)
	}
	return nil
}

// validOverlayAddress returns the first parseable IPv6 CIDR from the slice.
func validOverlayAddress(addrs []string) (string, bool) {
	for _, a := range addrs {
		if a == "" {
			continue
		}
		// Accept both a bare IPv6 (Core sends netip.Addr.String(), e.g.
		// "fdc9::...:1") and a prefixed form ("fd00::1/128"). The mere
		// absence of a mask must not cause the whole response to be
		// rejected — the address is what Core the address-authority assigns.
		addr, err := netip.ParseAddr(a)
		if err == nil {
			if addr.Is6() {
				return a, true
			}
			continue
		}
		prefix, err := netip.ParsePrefix(a)
		if err != nil {
			continue
		}
		if !prefix.Addr().Is6() {
			continue
		}
		return a, true
	}
	return "", false
}

// PairResult is the outcome of a successful pairing: the validated Core
// response and the durable membership derived from it.
type PairResult struct {
	Response   dollnetwork.PairResponse
	Membership *Membership
}

// PairWithInvitation runs the full M2 pairing pipeline:
//
//   - loads the persisted Body identity and WG keypair;
//   - consumes and validates the invitation (including expiry at `now`);
//   - chooses a bootstrap endpoint (direct HTTP(S));
//   - builds and submits the PairRequest to Core;
//   - validates Core's response as untrusted input;
//   - and, only if everything validates, commits the membership durably.
//
// `hc` is the HTTP client used to submit the PairRequest (the concrete
// transport — a real TCP client in production, an httptest-backed client in
// tests). `ctx` carries timeout/cancellation for the request. On success the
// returned membership is already persisted; callers must not persist on
// failure, and failure leaves any prior durable membership untouched.
//
// IMPORTANT: pairing FAILS CLOSED when a durable membership is already
// present. --pair must never silently replace an established Doll Network
// relationship. Replacement/reset/re-enrollment is an explicit (future)
// lifecycle operation, not implicit pairing behavior.
func PairWithInvitation(ctx context.Context, store *Store, inv *dollnetwork.Invitation, now int64, hc *http.Client) (*PairResult, error) {
	if store == nil {
		return nil, errors.New("body: pairing: nil store")
	}

	// Fail closed if a membership is already present or the file is corrupt/unreadable.
	// We must never replace an existing durable relationship just because --pair ran again.
	mem, err := store.LoadMembership()
	if err != nil {
		if errors.Is(err, ErrStateNotFound) {
			// No membership yet, pairing may continue.
		} else {
			// Any other error (corrupt file, IO, etc.) -> fail closed and preserve the file.
			return nil, &PairingError{Op: "load membership", Cause: err}
		}
	} else if mem != nil {
		// Valid existing membership -> reject implicit re-pairing.
		return nil, &PairingError{
			Op:    "existing membership",
			Cause: fmt.Errorf("member already paired (network_id=%q body_peer_id=%q); replacement is not implicit", mem.NetworkID, mem.BodyPeerID),
		}
	}

	if inv == nil {
		return nil, errors.New("body: pairing: nil invitation")
	}
	if hc == nil {
		return nil, errors.New("body: pairing: nil http client")
	}

	// Load persisted Body identity + WG keypair (M1).
	st, kp, err := store.LoadOrError()
	if err != nil {
		return nil, &PairingError{Op: "load body identity", Cause: err}
	}
	if st == nil || kp == nil {
		return nil, &PairingError{Op: "load body identity", Cause: errors.New("missing identity or wg keypair")}
	}

	// Validate the invitation structurally and against the current time.
	if err := validateInvitationShape(inv); err != nil {
		return nil, &PairingError{Op: "invitation validity", Cause: err}
	}
	if invitationExpired(inv, now) {
		return nil, &PairingError{Op: "invitation validity", Cause: fmt.Errorf("invitation %q expired at %s", inv.InvitationID, inv.ExpiresAt)}
	}

	ep, err := ChooseBootstrapEndpoint(inv)
	if err != nil {
		return nil, err
	}

	// Build the canonical PairRequest (includes the persisted Body ID and only
	// the public WG key — never the private key).
	req := BuildPairRequest(inv.InvitationID, inv.InvitationSecret, st, kp)
	if err := req.ValidatePairRequest(); err != nil {
		return nil, &PairingError{Op: "build pair request", Cause: err}
	}

	body, err := req.ToJSON()
	if err != nil {
		return nil, &PairingError{Op: "encode pair request", Cause: err}
	}

	// Build the pairing endpoint URL structurally so trailing slashes or a
	// bare origin cannot produce a malformed //v1/pair path. A bootstrap URL
	// of "http://host/" or "http://host" must both resolve to
	// "http://host/v1/pair".
	baseURL, err := url.Parse(ep.URL)
	if err != nil {
		return nil, &PairingError{Op: "parse bootstrap URL", Cause: err}
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + pairingEndpointPath
	baseURL.RawPath = ""
	pairURL := baseURL.String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pairURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, &PairingError{Op: "create pair request", Cause: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, &PairingError{Op: "send pair request", Cause: err}
	}
	defer resp.Body.Close()

	// Bound the response size before reading.
	limited := http.MaxBytesReader(nil, resp.Body, MaxPairingResponseBytes)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, &PairingError{Op: "read pairing response", Cause: err}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Non-2xx: try to surface the canonical structured pairing error.
		var perr dollnetwork.PairErrorResponse
		if len(raw) > 0 {
			if e2 := json.Unmarshal(raw, &perr); e2 == nil && perr.Reason != "" {
				msg := perr.Reason
				if perr.Error != "" {
					msg = perr.Error + " (" + perr.Reason + ")"
				}
				return nil, &PairingError{
					Op:    "pairing denied",
					Cause: errors.New("core error: " + msg),
				}
			}
		}
		return nil, &PairingError{
			Op:    "pairing denied",
			Cause: fmt.Errorf("http status %d", resp.StatusCode),
		}
	}

	// 2xx: parse + validate the membership response atomically.
	var pairResp dollnetwork.PairResponse
	if err := json.Unmarshal(raw, &pairResp); err != nil {
		return nil, &PairingError{Op: "validate pairing response", Cause: err}
	}
	if err := validatePairResponse(&pairResp); err != nil {
		return nil, &PairingError{Op: "validate pairing response", Cause: err}
	}

	// Everything validated: commit the durable membership. Only now does the
	// local membership become active.
	m := membershipFromResponse(&pairResp)
	if err := store.SaveMembership(&m); err != nil {
		return nil, &PairingError{Op: "persist membership", Cause: err}
	}

	return &PairResult{Response: pairResp, Membership: &m}, nil
}
