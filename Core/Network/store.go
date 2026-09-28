package network

import (
	"context"
)

// NetworkStore is the persistence boundary for Doll Network state.
// Network state is Core-local runtime bookkeeping — NOT part of
// canonical Doll State or Doll Card.
type NetworkStore interface {
	// SaveNetwork persists the entire Doll Network identity and Core identity.
	// SaveNetwork is expected to replace the previous value atomically.
	SaveNetwork(ctx context.Context, n *Network) error

	// LoadNetwork retrieves the last saved Doll Network and Core identity.
	// Returns nil, nil if no network has been saved yet (normal first-run).
	LoadNetwork(ctx context.Context) (*Network, error)

	// SaveMembership persists a single membership record.
	// INSERT OR REPLACE semantics — one membership per PeerID.
	SaveMembership(ctx context.Context, m *Membership) error

	// LoadMembership retrieves a single membership by PeerID.
	// Returns nil, nil if not found.
	LoadMembership(ctx context.Context, peerID PeerID) (*Membership, error)

	// ListMemberships retrieves all persisted membership records.
	// Returns an empty slice (not nil) when no memberships exist.
	ListMemberships(ctx context.Context) ([]*Membership, error)

	// DeleteMembership removes a membership record by PeerID.
	// No-op when the membership does not exist.
	DeleteMembership(ctx context.Context, peerID PeerID) error
}
