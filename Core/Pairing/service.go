package pairing

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── PairingService ───────────────────────────────────────────────────────────

// PairingService orchestrates the Body pairing flow.
// All HandlePairing calls are serialised by an internal mutex to
// protect the Network and Store from concurrent writes.
type PairingService struct {
	mu         sync.Mutex
	network    *network.Network
	netStore   network.NetworkStore
	invSvc     *invitation.Service
	authorizer Authorizer
	clock      invitation.Clock
	endpoints  dollnetwork.Endpoints
}

// NewService creates a PairingService.
func NewService(
	net *network.Network,
	netStore network.NetworkStore,
	invSvc *invitation.Service,
	authorizer Authorizer,
	clock invitation.Clock,
	coreEndpoints dollnetwork.Endpoints,
) *PairingService {
	if coreEndpoints == nil {
		coreEndpoints = dollnetwork.Endpoints{}
	}
	if authorizer == nil {
		authorizer = denyAuthorizer{}
	}
	return &PairingService{
		network:    net,
		netStore:   netStore,
		invSvc:     invSvc,
		authorizer: authorizer,
		clock:      clock,
		endpoints:  coreEndpoints,
	}
}

// denyAuthorizer denies every pairing — the safe default when no
// explicit Authorizer is provided.
type denyAuthorizer struct{}

func (denyAuthorizer) AuthorizePairing(_ context.Context, _ string, _ *dollnetwork.PairRequest) error {
	return fmt.Errorf("pairing: no authorizer configured")
}

// HandlePairing processes a PairRequest and returns either a success
// response or an error response.
//
//nolint:cyclop
func (s *PairingService) HandlePairing(ctx context.Context, req *dollnetwork.PairRequest) (*dollnetwork.PairResponse, *dollnetwork.PairErrorResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ── 1. Canonical request validation ───────────────────────────────────
	// The shared DollNetwork protocol is the validation boundary.
	if err := req.ValidatePairRequest(); err != nil {
		reason := "invalid_request"
		var verr *dollnetwork.ValidationError
		if errors.As(err, &verr) {
			reason = verr.Reason
		}
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    err.Error(),
			Reason:   reason,
			Consumed: false,
		}
	}

	// ── 2. Validate invitation ────────────────────────────────────────────
	inv, err := s.invSvc.Validate(ctx, req.InvitationID, req.Secret)
	if err != nil {
		// The invitation is invalid for one of: not found, consumed,
		// expired, wrong secret. Differentiate consumed vs. unknown.
		reason := "invalid_invitation"
		consumed := false
		if inv != nil && inv.Consumed {
			// The invitation exists and has already been consumed.
			reason = "invitation_already_consumed"
			consumed = true
		}
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("invitation rejected: %v", err),
			Reason:   reason,
			Consumed: consumed,
		}
	}

	// ── 3. Authorization ──────────────────────────────────────────────────
	if err := s.authorizer.AuthorizePairing(ctx, req.Body.BodyID, req); err != nil {
		// Denial consumes the invitation per protocol design.
		_ = s.invSvc.Consume(ctx, req.InvitationID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("pairing denied: %v", err),
			Reason:   "authorization_denied",
			Consumed: true,
		}
	}

	// ── 4. Decode Body WG public key at the domain boundary ─────────────
	rawWgKey, err := dollnetwork.DecodeWgPublicKey(req.Network.WireGuardPublicKey)
	if err != nil {
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    err.Error(),
			Reason:   "invalid_wg_public_key",
			Consumed: false,
		}
	}
	var wgPub network.WireGuardPublicKey
	copy(wgPub[:], rawWgKey)

	// ── 5. Create membership ──────────────────────────────────────────────
	m, err := s.network.NewMembership(req.Body.BodyID)
	if err != nil {
		// Possible errors: already exists, revoked, address collision.
		// Consume the invitation since the request was otherwise valid.
		_ = s.invSvc.Consume(ctx, req.InvitationID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("cannot create membership: %v", err),
			Reason:   "body_already_known",
			Consumed: true,
		}
	}

	// ── 6. Set Body WG public key ─────────────────────────────────────────
	if err := s.network.SetBodyWireGuardPublicKey(m.PeerID, wgPub); err != nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("failed to set public key: %v", err),
			Reason:   "key_assignment_failed",
			Consumed: false,
		}
	}

	// ── 7. Activate membership ────────────────────────────────────────────
	if err := s.network.ActivateMembership(m.PeerID); err != nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("failed to activate membership: %v", err),
			Reason:   "activation_failed",
			Consumed: false,
		}
	}

	// Re-read the membership now that it's active.
	active := s.network.GetMembership(m.PeerID)
	if active == nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    "membership lost after activation",
			Reason:   "internal_error",
			Consumed: false,
		}
	}

	// ── 8. Persist membership ─────────────────────────────────────────────
	if err := s.netStore.SaveMembership(ctx, active); err != nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("persistence error: %v", err),
			Reason:   "persistence_failed",
			Consumed: false,
		}
	}

	// ── 9. Consume invitation (transactional: membership already durable) ──
	if err := s.invSvc.Consume(ctx, req.InvitationID); err != nil {
		// Consumption failed — roll back membership.
		_ = s.network.RemoveMembership(m.PeerID)
		_ = s.netStore.DeleteMembership(ctx, m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("failed to consume invitation: %v", err),
			Reason:   "consumption_failed",
			Consumed: false,
		}
	}

	// ── 10. Encode Core WG public key at the domain boundary ──────────────
	coreWGKey, err := dollnetwork.EncodeWgPublicKey(s.network.Core.PublicKey[:])
	if err != nil {
		// Should never happen — Core's own key is valid.
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("encode core wg key: %v", err),
			Reason:   "internal_error",
			Consumed: true,
		}
	}

	// ── 11. Return canonical membership response ──────────────────────────
	return &dollnetwork.PairResponse{
		Version:         dollnetwork.ProtocolVersion,
		NetworkID:       string(s.network.NetworkID),
		BodyPeerID:      string(active.PeerID),
		BodyAddresses:   []string{active.OverlayAddress.String()},
		CorePeerID:      string(s.network.Core.PeerID),
		CoreWGPublicKey: coreWGKey,
		CoreAddresses:   []string{s.network.Core.OverlayAddress.String()},
		CoreEndpoints:   s.endpoints,
	}, nil
}