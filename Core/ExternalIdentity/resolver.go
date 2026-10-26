// Package externalidentity resolves identities observed by authenticated
// Gateway instances to Core-owned principals. Resolution does not authenticate
// a Gateway and does not itself authorize an action.
package externalidentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type GatewayInstanceID string
type PrincipalID string
type Authority string

const (
	// AuthorityParticipant identifies a known participant with no Owner or peer
	// authority. A verified platform identity defaults to no authority unless a
	// trusted mapping explicitly says otherwise.
	AuthorityParticipant Authority = "participant"
	AuthorityOwner       Authority = "owner"
	AuthorityPeer        Authority = "peer"
)

var (
	ErrInvalidMapping  = errors.New("external identity: invalid mapping")
	ErrInvalidIdentity = errors.New("external identity: invalid identity")
	ErrUnmapped        = errors.New("external identity: unmapped")
)

// Identity is the external identity tuple established at trusted ingress.
// GatewayInstanceID comes from the authenticated Gateway runtime, not a guest
// payload. Platform and SubjectID are opaque external namespace values.
type Identity struct {
	GatewayInstanceID GatewayInstanceID `json:"gateway_instance_id"`
	Platform          string            `json:"platform"`
	SubjectID         string            `json:"subject_id"`
}

// PresentedIdentity contains untrusted claims a platform event may carry.
// Claims are retained only so callers need not strip them before resolution;
// the resolver deliberately never consults them.
type PresentedIdentity struct {
	Identity Identity          `json:"identity"`
	Claims   map[string]string `json:"claims,omitempty"`
}

// Principal is a Core-resolved identity and its configured authority class.
// Authority is classification, not a decision to permit a particular action.
type Principal struct {
	ID        PrincipalID `json:"principal_id"`
	Authority Authority   `json:"authority"`
}

// Mapping is trusted Core configuration. Runtime callers cannot add or modify
// mappings through Resolver.
type Mapping struct {
	Identity  Identity
	Principal Principal
}

type Resolver interface {
	Resolve(context.Context, PresentedIdentity) (Principal, error)
}

// MemoryResolver is an immutable in-memory resolver built from trusted Core
// configuration. Exact tuple matching prevents one Gateway from borrowing a
// subject mapping established for another Gateway.
type MemoryResolver struct {
	mappings map[identityKey]Principal
}

type identityKey struct {
	gateway  GatewayInstanceID
	platform string
	subject  string
}

func NewMemoryResolver(mappings []Mapping) (*MemoryResolver, error) {
	resolved := make(map[identityKey]Principal, len(mappings))
	for index, mapping := range mappings {
		key, err := validatedKey(mapping.Identity)
		if err != nil || !validPrincipal(mapping.Principal) {
			return nil, fmt.Errorf("%w at index %d", ErrInvalidMapping, index)
		}
		if _, duplicate := resolved[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate identity at index %d", ErrInvalidMapping, index)
		}
		resolved[key] = Principal{
			ID:        PrincipalID(strings.TrimSpace(string(mapping.Principal.ID))),
			Authority: mapping.Principal.Authority,
		}
	}
	return &MemoryResolver{mappings: resolved}, nil
}

func (r *MemoryResolver) Resolve(ctx context.Context, presented PresentedIdentity) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if r == nil {
		return Principal{}, ErrInvalidIdentity
	}
	key, err := validatedKey(presented.Identity)
	if err != nil {
		return Principal{}, err
	}
	principal, ok := r.mappings[key]
	if !ok {
		return Principal{}, ErrUnmapped
	}
	return principal, nil
}

func validatedKey(identity Identity) (identityKey, error) {
	gateway := GatewayInstanceID(strings.TrimSpace(string(identity.GatewayInstanceID)))
	platform := strings.TrimSpace(identity.Platform)
	subject := strings.TrimSpace(identity.SubjectID)
	if gateway == "" || platform == "" || subject == "" {
		return identityKey{}, ErrInvalidIdentity
	}
	return identityKey{gateway: gateway, platform: platform, subject: subject}, nil
}

func validPrincipal(principal Principal) bool {
	if strings.TrimSpace(string(principal.ID)) == "" {
		return false
	}
	return principal.Authority == AuthorityParticipant || principal.Authority == AuthorityOwner || principal.Authority == AuthorityPeer
}

var _ Resolver = (*MemoryResolver)(nil)
