package enrollment

import (
	"bytes"
	"context"
	"crypto/rand"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

const authenticationChallengeLifetime = 30 * time.Second

type authenticationChallenge struct {
	connectionID string
	body         *BodyRecord
	value        authentication.Challenge
	timer        *time.Timer
}

func (s *Service) BeginAuthentication(ctx context.Context, connectionID string, hello authentication.Hello) (authentication.Challenge, error) {
	if connectionID == "" || hello.Version != authentication.Version || hello.Type != authentication.TypeHello || hello.BodyID == "" {
		return authentication.Challenge{}, ErrUnauthorized
	}
	if _, err := dollnetwork.DecodeNonce(hello.BodyNonce); err != nil || !supportsEd25519(hello.Algorithms) {
		return authentication.Challenge{}, ErrUnauthorized
	}
	body, err := s.store.GetBody(ctx, hello.BodyID)
	if err != nil || body == nil || body.RevokedAt != nil || !s.activeMembership(body) {
		return authentication.Challenge{}, ErrUnauthorized
	}
	coreNonce := make([]byte, 32)
	if _, err := rand.Read(coreNonce); err != nil {
		return authentication.Challenge{}, err
	}
	challenge := authentication.Challenge{
		Version: 1, Type: authentication.TypeChallenge, CoreID: s.identity.CoreID, BodyID: body.BodyID,
		SessionID: "session_" + uuid.NewString(), BodyNonce: hello.BodyNonce,
		CoreNonce: dollnetwork.EncodeRawURL(coreNonce), ExpiresAt: s.clock.Now().UTC().Add(authenticationChallengeLifetime).Truncate(time.Second),
		Algorithm: authentication.AlgorithmEd25519,
	}
	if err := authentication.SignChallenge(s.identity.PrivateKey, &challenge); err != nil {
		return authentication.Challenge{}, err
	}
	s.authMu.Lock()
	if previous := s.challenges[connectionID]; previous.timer != nil {
		previous.timer.Stop()
	}
	pending := authenticationChallenge{connectionID: connectionID, body: body, value: challenge}
	pending.timer = time.AfterFunc(authenticationChallengeLifetime, func() {
		s.authMu.Lock()
		if current, ok := s.challenges[connectionID]; ok && current.value.SessionID == challenge.SessionID {
			delete(s.challenges, connectionID)
		}
		s.authMu.Unlock()
	})
	s.challenges[connectionID] = pending
	s.authMu.Unlock()
	return challenge, nil
}

func (s *Service) CompleteAuthentication(ctx context.Context, connectionID string, proof authentication.Proof) (authentication.Accept, string, error) {
	s.authMu.Lock()
	pending, ok := s.challenges[connectionID]
	delete(s.challenges, connectionID) // every proof attempt consumes the challenge
	s.authMu.Unlock()
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if !ok || proof.Version != 1 || proof.Type != authentication.TypeProof {
		return authentication.Accept{}, "", ErrUnauthorized
	}
	challenge := pending.value
	if !s.clock.Now().Before(challenge.ExpiresAt) || proof.CoreID != challenge.CoreID || proof.BodyID != challenge.BodyID ||
		proof.SessionID != challenge.SessionID || proof.BodyNonce != challenge.BodyNonce || proof.CoreNonce != challenge.CoreNonce || proof.Algorithm != challenge.Algorithm {
		return authentication.Accept{}, "", ErrUnauthorized
	}
	body, err := s.store.GetBody(ctx, pending.body.BodyID)
	if err != nil || body == nil {
		return authentication.Accept{}, "", ErrUnauthorized
	}
	if body.RevokedAt != nil || !s.activeMembership(body) {
		return authentication.Accept{}, "", ErrRevoked
	}
	if body.KeyVersion != pending.body.KeyVersion || body.MembershipRef != pending.body.MembershipRef || !bytes.Equal(body.PublicKey, pending.body.PublicKey) {
		return authentication.Accept{}, "", ErrUnauthorized
	}
	if err := authentication.VerifyProof(body.PublicKey, proof); err != nil {
		return authentication.Accept{}, "", ErrInvalidProof
	}
	accept := authentication.Accept{Version: 1, Type: authentication.TypeAccept, CoreID: s.identity.CoreID, BodyID: body.BodyID, SessionID: challenge.SessionID, Status: "authenticated"}
	if err := authentication.SignAccept(s.identity.PrivateKey, &accept); err != nil {
		return authentication.Accept{}, "", err
	}
	return accept, pending.body.BodyID, nil
}

func (s *Service) CancelAuthentication(connectionID string) {
	s.authMu.Lock()
	if pending := s.challenges[connectionID]; pending.timer != nil {
		pending.timer.Stop()
	}
	delete(s.challenges, connectionID)
	s.authMu.Unlock()
}

func (s *Service) activeMembership(body *BodyRecord) bool {
	membership := s.network.GetMembership(network.PeerID(body.MembershipRef))
	return membership != nil && membership.BodyID == body.BodyID && membership.Status == network.MembershipActive
}

func supportsEd25519(algorithms []string) bool {
	for _, algorithm := range algorithms {
		if algorithm == authentication.AlgorithmEd25519 {
			return true
		}
	}
	return false
}
