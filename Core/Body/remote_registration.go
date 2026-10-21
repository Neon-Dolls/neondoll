package body

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

var (
	ErrRemoteMembershipUnknown = errors.New("remote body: unknown Doll Network membership")
	ErrRemoteMembershipRevoked = errors.New("remote body: Doll Network membership is revoked")
	ErrStaleRemoteSession      = errors.New("remote body: stale Doll Link session")
	ErrRemoteBodyDisconnected  = errors.New("remote body: no current Doll Link session")
)

// MembershipLister is the durable membership view used at the registration
// and execution boundaries. Network membership is authorization eligibility,
// never proof of the authenticated Body identity passed to SessionConnected.
type MembershipLister interface {
	ListMemberships(context.Context) ([]*network.Membership, error)
}

// RemoteRegistrar owns the production lifecycle of Registry RemoteBody
// entries. Session IDs are opaque connection generations assigned by Doll
// Link; Body IDs are accepted only after an external authenticator has
// verified them.
type RemoteRegistrar struct {
	mu        sync.Mutex
	registry  *Registry
	members   MembershipLister
	transport RemoteExecutionTransport
	sessions  map[BodyID]string
	remote    map[BodyID]*RemoteBody
}

func NewRemoteRegistrar(registry *Registry, members MembershipLister, transport RemoteExecutionTransport) (*RemoteRegistrar, error) {
	if registry == nil || members == nil || transport == nil {
		return nil, fmt.Errorf("remote body: registry, membership source, and transport are required")
	}
	return &RemoteRegistrar{
		registry: registry, members: members, transport: transport,
		sessions: make(map[BodyID]string), remote: make(map[BodyID]*RemoteBody),
	}, nil
}

// SessionConnected establishes the newest connection generation for an
// already-authenticated Body. It verifies active membership and creates the
// stable Registry entry with no available capabilities until advertisement.
func (r *RemoteRegistrar) SessionConnected(ctx context.Context, sessionID, authenticatedBodyID string) error {
	if sessionID == "" || authenticatedBodyID == "" {
		return fmt.Errorf("remote body: session and authenticated Body identity are required")
	}
	bodyID := BodyID(authenticatedBodyID)
	if err := r.requireActiveMembership(ctx, bodyID); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	remote := r.remote[bodyID]
	if remote == nil {
		if existing, exists := r.registry.Get(bodyID); exists {
			var ok bool
			remote, ok = existing.(*RemoteBody)
			if !ok {
				return fmt.Errorf("remote body %q conflicts with registered %s Body", bodyID, existing.Kind())
			}
			remote.setTransport(r)
		} else {
			var err error
			remote, err = NewRemote(bodyID, authenticatedBodyID, nil, r)
			if err != nil {
				return err
			}
			if err := r.registry.Register(remote); err != nil {
				return err
			}
		}
		r.remote[bodyID] = remote
	}
	r.sessions[bodyID] = sessionID
	remote.setDisconnected()
	return nil
}

// CapabilitiesAdvertised atomically replaces one current session's complete
// advertisement. A stale connection can neither restore availability nor
// overwrite the newer session's capability set.
func (r *RemoteRegistrar) CapabilitiesAdvertised(ctx context.Context, sessionID, authenticatedBodyID string, advertised []capabilities.Capability) error {
	bodyID := BodyID(authenticatedBodyID)
	if err := r.requireActiveMembership(ctx, bodyID); err != nil {
		return err
	}
	mapped := make([]Capability, len(advertised))
	for i, capability := range advertised {
		mapped[i] = Capability{
			ID: capability.ID, Operations: append([]string(nil), capability.Operations...), Available: capability.Available,
			Constraints: cloneStringMap(capability.Constraints), Metadata: cloneStringMap(capability.Metadata),
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[bodyID] != sessionID {
		return ErrStaleRemoteSession
	}
	remote := r.remote[bodyID]
	if remote == nil {
		return ErrRemoteBodyDisconnected
	}
	return remote.replaceCapabilities(mapped, true)
}

// SessionDisconnected marks capabilities unavailable only if sessionID is
// still the current generation. Disconnect from a replaced connection is a
// no-op.
func (r *RemoteRegistrar) SessionDisconnected(sessionID, authenticatedBodyID string) {
	bodyID := BodyID(authenticatedBodyID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[bodyID] != sessionID {
		return
	}
	delete(r.sessions, bodyID)
	if remote := r.remote[bodyID]; remote != nil {
		remote.setDisconnected()
	}
}

// Execute enforces current membership and session availability before
// delegating to the existing Doll Link correlation transport.
func (r *RemoteRegistrar) Execute(ctx context.Context, bodyID, dollID string, payload execution.RequestPayload) (execution.Result, error) {
	id := BodyID(bodyID)
	if err := r.requireActiveMembership(ctx, id); err != nil {
		return execution.Result{}, err
	}
	r.mu.Lock()
	connected := r.sessions[id] != ""
	r.mu.Unlock()
	if !connected {
		return execution.Result{}, fmt.Errorf("%w for Body %q", ErrRemoteBodyDisconnected, bodyID)
	}
	return r.transport.Execute(ctx, bodyID, dollID, payload)
}

func (r *RemoteRegistrar) requireActiveMembership(ctx context.Context, bodyID BodyID) error {
	memberships, err := r.members.ListMemberships(ctx)
	if err != nil {
		return fmt.Errorf("remote body: list memberships: %w", err)
	}
	for _, membership := range memberships {
		if membership == nil || BodyID(membership.BodyID) != bodyID {
			continue
		}
		if membership.Status == network.MembershipRevoked {
			return fmt.Errorf("%w: %q", ErrRemoteMembershipRevoked, bodyID)
		}
		if membership.Status != network.MembershipActive {
			return fmt.Errorf("%w: %q is %s", ErrRemoteMembershipUnknown, bodyID, membership.Status)
		}
		return nil
	}
	return fmt.Errorf("%w: %q", ErrRemoteMembershipUnknown, bodyID)
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
