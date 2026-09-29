// Package body — reusable, Core-independent reference Body runtime.
//
// M2 — Body Pairs.
//
// This file implements the pipeline a fresh Body runs to consume a Core
// pairing invitation, pair through the canonical direct HTTP(S) bootstrap,
// validate Core's membership response, and persist the resulting Doll Network
// membership:
//
//   1. Consume & validate the invitation (version, id, secret, expiry,
//      advertised bootstrap endpoints).
//   2. Choose a supported bootstrap endpoint. As of M2 only direct HTTP(S)
//      bootstrap is implemented; a relay endpoint is representable but fails
//      explicitly as unsupported rather than silently behaving like direct.
//   3. Build the canonical DollNetwork.PairRequest from the persisted Body
//      identity + WG keypair, and submit it to Core over HTTP(S).
//   4. Treat Core's response as untrusted network input: validate the
//      protocol version, required membership fields, IPv6 address/prefixes,
//      the Core WG public key (canonical DecodeWgPublicKey), and the
//      structural form of any endpoint entries.
//   5. Commit the membership durably ONLY after the entire response validates
//      (atomic: no partial durable state). The invitation secret is never
//      persisted and never logged.
//
// The reusable pairing logic lives here, outside cmd/neondoll-body (which
// only parses inputs and invokes it).

package body

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"net/http"

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

// LoadInvitation parses an invitation document and validates version and
// required fields. It does NOT verify expiry — callers pass a current time to
// ValidInvitationAt. Returns ErrStateNotFound-style errors wrapped as
// PairingError on malformed input.
func LoadInvitation(raw string) (*dollnetwork.Invitation, error) {
	var inv dollnetwork.Invitation
	if err := inv.FromJSON([]byte(raw)); err != nil {
		return nil, &PairingError{Op: "parse invitation", Cause: err}
	}
	return &inv, nil
}

// ChooseBootstrapEndpoint selects a supported bootstrap endpoint from the
// invitation. As of M2 the Body implements direct HTTP(S) bootstrap; a relay
// endpoint is representable but unsupported, so if no direct endpoint exists
// we fail explicitly rather than treating relay like direct.
func ChooseBootstrapEndpoint(inv *dollnetwork.Invitation) (*dollnetwork.BootstrapEndpoint, error) {
	if inv == nil {
		return nil, errors.New("body: pairing: nil invitation")
	}
	for _, e := range inv.BootstrapEndpoints {
		if e.Type == dollnetwork.EndpointDirect {
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
		BodyIPv6:     resp.BodyIPv6,
		CorePeerID:   resp.CorePeerID,
		CoreWGKeyB64: resp.CoreWGKeyB64,
	}
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
func PairWithInvitation(ctx context.Context, store *Store, inv *dollnetwork.Invitation, now int64, hc *http.Client) (*PairResult, error) {
	if store == nil {
		return nil, errors.New("body: pairing: nil store")
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

	// Validate the invitation against the current time.
	if err := inv.ValidInvitationAt(now); err != nil {
		return nil, &PairingError{Op: "invitation validity", Cause: err}
	}

	ep, err := ChooseBootstrapEndpoint(inv)
	if err != nil {
		return nil, err
	}

	// Build the canonical PairRequest (includes the persisted Body ID and only
	// the public WG key — never the private key).
	req := BuildPairRequest(string(inv.ID), string(inv.Secret), st, kp)
	if err := req.ValidatePairRequest(); err != nil {
		return nil, &PairingError{Op: "build pair request", Cause: err}
	}

	body, err := req.ToJSON()
	if err != nil {
		return nil, &PairingError{Op: "encode pair request", Cause: err}
	}

	url := string(ep.BootstrapURL) + dollnetwork.PairingPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
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
		var perr dollnetwork.PairingError
		if e2 := perr.FromJSON(raw); e2 == nil {
			return nil, &PairingError{
				Op:    "pairing denied",
				Cause: errors.New(fmt.Sprintf("core error %s: %s", string(perr.Code), string(perr.Message))),
			}
		}
		return nil, &PairingError{
			Op:    "pairing denied",
			Cause: errors.New(fmt.Sprintf("http status %d", resp.StatusCode)),
		}
	}

	// 2xx: parse + validate the membership response atomically.
	var pairResp dollnetwork.PairResponse
	if err := pairResp.FromJSON(raw); err != nil {
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