package enrollment

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type Service struct {
	mu sync.Mutex
	// lifecycleMu keeps proof acceptance plus transport activation atomic with
	// revocation. The lifecycle lock is acquired before mu; authentication
	// challenge state is independently protected by authMu.
	lifecycleMu sync.RWMutex
	authMu      sync.Mutex
	challenges  map[string]authenticationChallenge
	store       Store
	network     *network.Network
	invitations *invitation.Service
	clock       Clock
	config      Config
	identity    *CoreIdentity
	owners      OwnerAuthorizer
	sessions    SessionRevoker
	peers       PeerRefresher
}

func (s *Service) SetRevocationLifecycle(sessions SessionRevoker, peers PeerRefresher) {
	s.mu.Lock()
	s.sessions = sessions
	s.peers = peers
	s.mu.Unlock()
}

func (s *Service) ListPending(ctx context.Context, ownerID string) ([]*PendingEnrollment, error) {
	if ownerID == "" || s.owners.AuthorizeEnrollment(ctx, ownerID, OwnerAction{Operation: "list_pending"}) != nil {
		return nil, ErrUnauthorized
	}
	items, err := s.store.ListPending(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	result := make([]*PendingEnrollment, 0, len(items))
	for _, item := range items {
		if item.Status == StatusPending && !now.Before(item.ExpiresAt) {
			continue
		}
		if item.Status != StatusPending {
			continue
		}
		item.CodeVerifier = nil
		item.BodyNonce = nil
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) ListBodies(ctx context.Context, ownerID string) ([]*BodyRecord, error) {
	if ownerID == "" || s.owners.AuthorizeEnrollment(ctx, ownerID, OwnerAction{Operation: "list_bodies"}) != nil {
		return nil, ErrUnauthorized
	}
	bodies, err := s.store.ListBodies(ctx)
	if err != nil {
		return nil, err
	}
	for _, body := range bodies {
		body.Status = string(StatusEnrolled)
		if body.RevokedAt != nil {
			body.Status = "revoked"
		}
	}
	return bodies, nil
}

func (s *Service) Revoke(ctx context.Context, bodyID, ownerID string) error {
	if bodyID == "" || ownerID == "" {
		return ErrUnauthorized
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.owners.AuthorizeEnrollment(ctx, ownerID, OwnerAction{Operation: "revoke", RequestID: bodyID}) != nil {
		return ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := s.store.GetBody(ctx, bodyID)
	if err != nil {
		return err
	}
	if body == nil {
		return ErrUnknownBody
	}
	membership := s.network.GetMembership(network.PeerID(body.MembershipRef))
	if membership == nil || membership.BodyID != bodyID {
		return ErrUnknownBody
	}
	if body.RevokedAt == nil {
		copy := *membership
		copy.Status = network.MembershipRevoked
		revokedAt := s.clock.Now().UTC()
		if err := s.store.RevokeBody(ctx, bodyID, &copy, revokedAt); err != nil {
			return err
		}
	}
	if membership.Status != network.MembershipRevoked {
		if err := s.network.RevokeMembership(membership.PeerID); err != nil && !errors.Is(err, network.ErrMembershipRevoked) {
			return err
		}
	}
	if s.sessions != nil {
		s.sessions.DisconnectBody(bodyID)
	}
	if s.peers != nil {
		if err := s.peers.RefreshPeers(ctx); err != nil {
			return err
		}
	}
	return nil
}

func NewService(ctx context.Context, store Store, nw *network.Network, invitations *invitation.Service, owners OwnerAuthorizer, clock Clock, config Config) (*Service, error) {
	if store == nil || nw == nil || invitations == nil {
		return nil, errors.New("enrollment: store, network, and invitation service are required")
	}
	if clock == nil {
		clock = realClock{}
	}
	if owners == nil {
		owners = denyOwnerAuthorizer{}
	}
	if config.PendingLifetime <= 0 || config.MaxPending <= 0 || config.MaxAttempts <= 0 {
		config = DefaultConfig()
	}
	identity, err := store.LoadCoreIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("enrollment: generate Core identity: %w", err)
		}
		identity = &CoreIdentity{CoreID: "core_" + uuid.NewString(), PublicKey: publicKey, PrivateKey: privateKey}
		if err := store.SaveCoreIdentity(ctx, identity); err != nil {
			return nil, fmt.Errorf("enrollment: persist Core identity: %w", err)
		}
	}
	return &Service{store: store, network: nw, invitations: invitations, clock: clock, config: config, identity: identity, owners: owners, challenges: make(map[string]authenticationChallenge)}, nil
}

func (s *Service) CoreIdentity() CoreIdentity {
	return CoreIdentity{CoreID: s.identity.CoreID, PublicKey: append(ed25519.PublicKey(nil), s.identity.PublicKey...)}
}

func (s *Service) PublicCoreIdentity() dollnetwork.CoreIdentityResponse {
	return dollnetwork.CoreIdentityResponse{
		Version: dollnetwork.CoreIdentityVersion, CoreID: s.identity.CoreID,
		Algorithm: dollnetwork.CoreIdentityAlgorithm, PublicKey: dollnetwork.EncodeRawURL(s.identity.PublicKey),
	}
}

func (s *Service) Begin(ctx context.Context, request dollnetwork.EnrollmentBeginRequest) (*dollnetwork.EnrollmentPendingResponse, error) {
	if request.Version != dollnetwork.EnrollmentVersion {
		return nil, ErrUnsupportedVersion
	}
	if request.CoreID != s.identity.CoreID {
		return nil, ErrCoreIdentity
	}
	if err := dollnetwork.VerifyEnrollmentBegin(request); err != nil {
		return nil, ErrInvalidProof
	}
	return s.createPending(ctx, request.RequestID, ModeBodyRequest, request.BodyPublicKey, request.WGPublicKey, request.BodyNonce, "", request.Metadata, true)
}

func (s *Service) Redeem(ctx context.Context, request dollnetwork.EnrollmentRedeemRequest) (*dollnetwork.EnrollmentReceipt, error) {
	if request.Version != dollnetwork.EnrollmentVersion {
		return nil, ErrUnsupportedVersion
	}
	if request.CoreID != s.identity.CoreID {
		return nil, ErrCoreIdentity
	}
	if err := dollnetwork.VerifyEnrollmentRedeem(request); err != nil {
		return nil, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.store.GetPending(ctx, request.RequestID); err == nil && existing != nil {
		publicKey, decodeErr := dollnetwork.DecodeBodyPublicKey(request.BodyPublicKey)
		if decodeErr != nil || !hmac.Equal(existing.BodyPublicKey, publicKey) || existing.Mode != ModeInvitation || existing.InvitationID != request.InvitationID {
			return nil, ErrReplayed
		}
		inv, err := s.invitations.ValidateAuthorization(ctx, request.InvitationID, request.InvitationSecret)
		if err != nil {
			return nil, ErrUnauthorized
		}
		if existing.Status == StatusEnrolled {
			return s.store.GetReceipt(ctx, request.RequestID)
		}
		inv, err = s.invitations.Validate(ctx, request.InvitationID, request.InvitationSecret)
		if err != nil {
			return nil, ErrUnauthorized
		}
		return s.completeLocked(ctx, existing, "invitation", inv.Permissions)
	}
	inv, err := s.invitations.Validate(ctx, request.InvitationID, request.InvitationSecret)
	if err != nil {
		return nil, ErrUnauthorized
	}
	pending, err := s.newPending(request.RequestID, ModeInvitation, request.BodyPublicKey, request.WGPublicKey, request.BodyNonce, request.InvitationID, request.Metadata, nil)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreatePending(ctx, pending); err != nil {
		if receipt, getErr := s.store.GetReceipt(ctx, request.RequestID); getErr == nil && receipt != nil {
			return receipt, nil
		}
		return nil, ErrReplayed
	}
	return s.completeLocked(ctx, pending, "invitation", inv.Permissions)
}

func (s *Service) Approve(ctx context.Context, code, ownerID string, permissions []string) (*dollnetwork.EnrollmentReceipt, error) {
	if ownerID == "" {
		return nil, ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.store.GetPendingByCodeVerifier(ctx, s.codeVerifier(code))
	if err != nil || pending == nil {
		return nil, ErrUnauthorized
	}
	if pending.AttemptCount >= s.config.MaxAttempts {
		return nil, ErrRateLimited
	}
	if err := s.owners.AuthorizeEnrollment(ctx, ownerID, OwnerAction{Operation: "approve", RequestID: pending.RequestID}); err != nil {
		return nil, ErrUnauthorized
	}
	return s.completeLocked(ctx, pending, ownerID, permissions)
}

func (s *Service) Deny(ctx context.Context, code, ownerID string) error {
	if ownerID == "" {
		return ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.store.GetPendingByCodeVerifier(ctx, s.codeVerifier(code))
	if err != nil || pending == nil {
		return ErrUnauthorized
	}
	if err := s.owners.AuthorizeEnrollment(ctx, ownerID, OwnerAction{Operation: "deny", RequestID: pending.RequestID}); err != nil {
		return ErrUnauthorized
	}
	if s.clock.Now().After(pending.ExpiresAt) {
		return ErrExpired
	}
	return s.store.Deny(ctx, pending.RequestID, ownerID)
}

func (s *Service) Result(ctx context.Context, request dollnetwork.EnrollmentResultRequest) (*dollnetwork.EnrollmentResultResponse, error) {
	if request.Version != dollnetwork.EnrollmentVersion {
		return nil, ErrUnsupportedVersion
	}
	if request.CoreID != s.identity.CoreID {
		return nil, ErrCoreIdentity
	}
	pending, err := s.store.GetPending(ctx, request.RequestID)
	if err != nil || pending == nil {
		return nil, ErrUnknownRequest
	}
	if pending.AttemptCount >= s.config.MaxAttempts {
		return nil, ErrRateLimited
	}
	if err := dollnetwork.VerifyEnrollmentResult(request); err != nil || !hmac.Equal(pending.BodyPublicKey, mustPublicKey(request.BodyPublicKey)) {
		_ = s.store.IncrementAttempts(ctx, request.RequestID)
		return nil, ErrInvalidProof
	}
	nonce, err := dollnetwork.DecodeNonce(request.BodyNonce)
	if err != nil {
		return nil, ErrInvalidProof
	}
	consumed, err := s.store.ConsumeResultNonce(ctx, request.RequestID, nonce)
	if err != nil {
		return nil, err
	}
	if !consumed {
		return nil, ErrReplayed
	}
	status := pending.Status
	if status == StatusPending && !s.clock.Now().Before(pending.ExpiresAt) {
		status = StatusExpired
	}
	response := &dollnetwork.EnrollmentResultResponse{Version: 1, RequestID: request.RequestID, Status: string(status)}
	if status == StatusEnrolled {
		receipt, err := s.store.GetReceipt(ctx, request.RequestID)
		if err != nil || receipt == nil {
			return nil, errors.New("enrollment: committed receipt unavailable")
		}
		response.Receipt = receipt
	}
	return response, nil
}

func (s *Service) createPending(ctx context.Context, requestID string, mode Mode, bodyKey, wgKey, nonce, invitationID string, metadata dollnetwork.EnrollmentMetadata, includeCode bool) (*dollnetwork.EnrollmentPendingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count, err := s.store.CountPending(ctx, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if count >= s.config.MaxPending {
		return nil, ErrRateLimited
	}
	code := ""
	var verifier []byte
	if includeCode {
		code, err = generateCode()
		if err != nil {
			return nil, err
		}
		verifier = s.codeVerifier(code)
	}
	pending, err := s.newPending(requestID, mode, bodyKey, wgKey, nonce, invitationID, metadata, verifier)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreatePending(ctx, pending); err != nil {
		return nil, ErrReplayed
	}
	coreNonce := make([]byte, 32)
	if _, err := rand.Read(coreNonce); err != nil {
		return nil, err
	}
	response := &dollnetwork.EnrollmentPendingResponse{
		Version: 1, RequestID: requestID, CoreID: s.identity.CoreID,
		BodyKeyFingerprint: dollnetwork.BodyKeyFingerprint(pending.BodyPublicKey),
		CoreNonce:          dollnetwork.EncodeRawURL(coreNonce), ApprovalCode: code, ExpiresAt: pending.ExpiresAt,
	}
	if err := dollnetwork.SignEnrollmentPending(s.identity.PrivateKey, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) newPending(requestID string, mode Mode, bodyKey, wgKey, nonce, invitationID string, metadata dollnetwork.EnrollmentMetadata, verifier []byte) (*PendingEnrollment, error) {
	if requestID == "" || len(requestID) > 128 {
		return nil, errors.New("enrollment: invalid request id")
	}
	metadataBytes := 0
	for key, value := range metadata {
		metadataBytes += len([]byte(key)) + len([]byte(value))
	}
	if metadataBytes > 1024 {
		return nil, errors.New("enrollment: metadata exceeds 1024 bytes")
	}
	publicKey, err := dollnetwork.DecodeBodyPublicKey(bodyKey)
	if err != nil {
		return nil, ErrInvalidProof
	}
	wg, err := dollnetwork.DecodeWgPublicKey(wgKey)
	if err != nil {
		return nil, ErrInvalidProof
	}
	bodyNonce, err := dollnetwork.DecodeNonce(nonce)
	if err != nil {
		return nil, ErrInvalidProof
	}
	var networkKey network.WireGuardPublicKey
	copy(networkKey[:], wg)
	// Wire timestamps are signed as their exact RFC3339 field. Restrict them
	// to whole seconds so JSON transport cannot introduce a fractional form
	// different from the transcript string.
	now := s.clock.Now().UTC().Truncate(time.Second)
	return &PendingEnrollment{
		RequestID: requestID, Mode: mode, CoreID: s.identity.CoreID, BodyPublicKey: publicKey,
		WGPublicKey: networkKey, BodyNonce: bodyNonce, CodeVerifier: verifier, InvitationID: invitationID,
		Metadata: metadata, Status: StatusPending, CreatedAt: now, ExpiresAt: now.Add(s.config.PendingLifetime),
	}, nil
}

func (s *Service) completeLocked(ctx context.Context, pending *PendingEnrollment, ownerID string, permissions []string) (*dollnetwork.EnrollmentReceipt, error) {
	if pending.Status == StatusEnrolled {
		return s.store.GetReceipt(ctx, pending.RequestID)
	}
	if pending.Status == StatusDenied {
		return nil, ErrDenied
	}
	if !s.clock.Now().Before(pending.ExpiresAt) {
		return nil, ErrExpired
	}
	bodyID := "body_" + uuid.NewString()
	membership, err := s.network.NewMembership(bodyID)
	if err != nil {
		return nil, err
	}
	rollback := func() { _ = s.network.RemoveMembership(membership.PeerID) }
	if err := s.network.SetBodyWireGuardPublicKey(membership.PeerID, pending.WGPublicKey); err != nil {
		rollback()
		return nil, err
	}
	if err := s.network.ActivateMembership(membership.PeerID); err != nil {
		rollback()
		return nil, err
	}
	now := s.clock.Now().UTC().Truncate(time.Second)
	body := &BodyRecord{
		BodyID: bodyID, PublicKey: append(ed25519.PublicKey(nil), pending.BodyPublicKey...), KeyVersion: 1,
		OwnerID: ownerID, Permissions: append([]string(nil), permissions...), MembershipRef: string(membership.PeerID),
		CreatedAt: now, UpdatedAt: now,
	}
	receipt := &dollnetwork.EnrollmentReceipt{
		Version: 1, RequestID: pending.RequestID, Status: dollnetwork.EnrollmentStatusEnrolled,
		BodyID: bodyID, CoreID: s.identity.CoreID,
		BodyPublicKeyFingerprint: dollnetwork.BodyKeyFingerprint(pending.BodyPublicKey),
		Permissions:              append([]string(nil), permissions...), MembershipReference: string(membership.PeerID), IssuedAt: now,
	}
	if err := dollnetwork.SignEnrollmentReceipt(s.identity.PrivateKey, receipt); err != nil {
		rollback()
		return nil, err
	}
	pending.Status = StatusEnrolled
	pending.ApprovedOwnerID = ownerID
	pending.Permissions = append([]string(nil), permissions...)
	if err := s.store.Complete(ctx, &Completion{Pending: pending, Body: body, Membership: membership, Receipt: receipt}); err != nil {
		rollback()
		if existing, getErr := s.store.GetReceipt(ctx, pending.RequestID); getErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return receipt, nil
}

func (s *Service) codeVerifier(code string) []byte {
	mac := hmac.New(sha256.New, s.identity.PrivateKey.Seed())
	mac.Write([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return mac.Sum(nil)
}

func generateCode() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return encoded[:4] + "-" + encoded[4:8] + "-" + encoded[8:], nil
}

func mustPublicKey(encoded string) []byte {
	key, _ := dollnetwork.DecodeBodyPublicKey(encoded)
	return key
}
