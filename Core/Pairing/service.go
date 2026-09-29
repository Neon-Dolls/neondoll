package pairing

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── WG key helpers ───────────────────────────────────────────────────────────

var errKeyDecode = fmt.Errorf("body_wg_public_key: must be 32 bytes encoded as standard base64")

// decodeWGPublicKey decodes a base64-encoded WireGuard public key (32 bytes).
func decodeWGPublicKey(encoded string) (*network.WireGuardPublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errKeyDecode, err)
	}
	if len(decoded) != 32 {
		return nil, errKeyDecode
	}
	var key network.WireGuardPublicKey
	copy(key[:], decoded)
	return &key, nil
}

// encodeWGPublicKey encodes a WireGuard public key as standard base64.
func encodeWGPublicKey(key network.WireGuardPublicKey) string {
	return base64.StdEncoding.EncodeToString(key[:])
}

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
	endpoints  []string
}

// NewService creates a PairingService.
func NewService(
	net *network.Network,
	netStore network.NetworkStore,
	invSvc *invitation.Service,
	authorizer Authorizer,
	clock invitation.Clock,
	coreEndpoints []string,
) *PairingService {
	if coreEndpoints == nil {
		coreEndpoints = []string{}
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

// HandlePairing processes a PairRequest and returns either a success
// response or an error response.
func (s *PairingService) HandlePairing(ctx context.Context, req *dollnetwork.PairRequest) (*dollnetwork.PairResponse, *dollnetwork.PairErrorResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ── 1. Validate protocol version ──────────────────────────────────────
	if req.Version != dollnetwork.ProtocolVersion {
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    "unsupported protocol version",
			Reason:   "unsupported_version",
			Consumed: false,
		}
	}

	// ── 2. Validate invitation ────────────────────────────────────────────
	_, err := s.invSvc.Validate(ctx, req.InvitationID, req.Secret)
	if err != nil {
		// The invitation is invalid for one of: not found, consumed,
		// expired, wrong secret. All are handled uniformly.
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("invitation rejected: %v", err),
			Reason:   "invalid_invitation",
			Consumed: false, // optimistic; we don't check consumed state here
		}
	}

	// ── 3. Validate Body metadata ─────────────────────────────────────────
	if req.Body.BodyID == "" {
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    "body_id is required",
			Reason:   "missing_body_id",
			Consumed: false,
		}
	}

	// ── 4. Decode/validate Body WG public key ─────────────────────────────
	wgPub, err := decodeWGPublicKey(req.Network.WireGuardPublicKey)
	if err != nil {
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    err.Error(),
			Reason:   "invalid_wg_public_key",
			Consumed: false,
		}
	}

	// ── 5. Authorization ──────────────────────────────────────────────────
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

	// ── 6. Create membership ──────────────────────────────────────────────
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

	// ── 7. Set Body WG public key ─────────────────────────────────────────
	if err := s.network.SetBodyWireGuardPublicKey(m.PeerID, *wgPub); err != nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("failed to set public key: %v", err),
			Reason:   "key_assignment_failed",
			Consumed: false,
		}
	}

	// ── 8. Activate membership ────────────────────────────────────────────
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

	// ── 9. Persist membership ─────────────────────────────────────────────
	if err := s.netStore.SaveMembership(ctx, active); err != nil {
		_ = s.network.RemoveMembership(m.PeerID)
		return nil, &dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    fmt.Sprintf("persistence error: %v", err),
			Reason:   "persistence_failed",
			Consumed: false,
		}
	}

	// ── 10. Consume invitation ────────────────────────────────────────────
	_ = s.invSvc.Consume(ctx, req.InvitationID)

	// ── 11. Return canonical membership response ──────────────────────────
	return &dollnetwork.PairResponse{
		Version:         dollnetwork.ProtocolVersion,
		NetworkID:       string(s.network.NetworkID),
		BodyPeerID:      string(active.PeerID),
		BodyAddresses:   []string{active.OverlayAddress.String()},
		CorePeerID:      string(s.network.Core.PeerID),
		CoreWGPublicKey: encodeWGPublicKey(s.network.Core.PublicKey),
		CoreAddresses:   []string{s.network.Core.OverlayAddress.String()},
		CoreEndpoints:   s.endpoints,
	}, nil
}
