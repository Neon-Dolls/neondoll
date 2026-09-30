package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/Neon-Dolls/neondoll/Core/Network"
)

// NetworkStore errors.
var (
	// ErrNoNetworkState is returned when no network state has been saved yet.
	// This is the normal first-run case — NOT an error to be fatal.
	ErrNoNetworkState = fmt.Errorf("persistence: no network state saved")
)

// Ensure *store implements network.NetworkStore.
var _ network.NetworkStore = (*store)(nil)

// networkStateRow is the internal JSON serialization form for the
// Core's network and peer identity. This is a storage format, NOT a
// protocol or Doll Card structure. Keys in the table:
//
//	"network:identity"   → network_id + core_identity
//	"network:membership:<peer_id>" → each membership
//
// The database-level split (one row for identity, one row per membership)
// allows concurrent partial updates without serializing the full Network
// struct every time.
const (
	networkIdentityKey = "network:identity"
)

// networkIdentityRow is the JSON shape stored under the "network:identity" key.
// It combines the Doll Network identity with Core's peer identity and WG keys
// in a single row because these are written together on first initialization.
type networkIdentityRow struct {
	NetworkID       network.NetworkID `json:"network_id"`
	CorePeerID      network.PeerID    `json:"core_peer_id"`
	CorePrivateKey  []byte            `json:"core_private_key"`
	CorePublicKey   []byte            `json:"core_public_key"`
	CoreOverlayAddr string            `json:"core_overlay_addr"`
}

// membershipRow is the JSON shape stored under each "network:membership:<peer_id>" key.
type membershipRow struct {
	BodyID             string                   `json:"body_id"`
	PeerID             network.PeerID           `json:"peer_id"`
	WireGuardPublicKey []byte                   `json:"wg_public_key,omitempty"`
	OverlayAddr        string                   `json:"overlay_addr"`
	Status             network.MembershipStatus `json:"status"`
}

// SaveNetwork persists the Doll Network identity and Core identity atomically.
func (s *store) SaveNetwork(ctx context.Context, n *network.Network) error {
	if n == nil {
		return fmt.Errorf("persistence: save network: nil network")
	}

	row := networkIdentityRow{
		NetworkID:       n.NetworkID,
		CorePeerID:      n.Core.PeerID,
		CorePrivateKey:  n.Core.PrivateKey[:],
		CorePublicKey:   n.Core.PublicKey[:],
		CoreOverlayAddr: n.Core.OverlayAddress.String(),
	}

	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("%w: marshal network identity: %v", ErrCannotSave, err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO network_state (key, value, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		networkIdentityKey, string(data),
	)
	if err != nil {
		return fmt.Errorf("%w: save network identity: %v", ErrCannotSave, err)
	}
	return nil
}

// LoadNetwork retrieves the last saved Doll Network identity and Core identity.
// Returns nil, nil if no network has been saved yet.
func (s *store) LoadNetwork(ctx context.Context) (*network.Network, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM network_state WHERE key = ?`, networkIdentityKey,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load network identity: %v", ErrCannotDecode, err)
	}

	var row networkIdentityRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		return nil, fmt.Errorf("%w: unmarshal network identity: %v", ErrCannotDecode, err)
	}

	// Reconstruct CoreIdentity.
	coreAddr, err := netip.ParseAddr(row.CoreOverlayAddr)
	if err != nil {
		return nil, fmt.Errorf("%w: parse core overlay addr %q: %v", ErrCannotDecode, row.CoreOverlayAddr, err)
	}

	var privKey network.WireGuardPrivateKey
	if n := copy(privKey[:], row.CorePrivateKey); n != 32 {
		return nil, fmt.Errorf("%w: core private key length %d != 32", ErrCannotDecode, n)
	}

	var pubKey network.WireGuardPublicKey
	if n := copy(pubKey[:], row.CorePublicKey); n != 32 {
		return nil, fmt.Errorf("%w: core public key length %d != 32", ErrCannotDecode, n)
	}

	n := &network.Network{
		NetworkID: row.NetworkID,
		Core: network.CoreIdentity{
			PeerID:         row.CorePeerID,
			PrivateKey:     privKey,
			PublicKey:      pubKey,
			OverlayAddress: coreAddr,
		},
		Memberships: make(map[network.PeerID]*network.Membership),
	}

	// Populate persisted memberships so the reconstructed Network
	// is immediately usable and knows about existing/revoked Bodies.
	mems, err := s.ListMemberships(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: load memberships: %v", ErrCannotDecode, err)
	}
	for _, m := range mems {
		n.Memberships[m.PeerID] = m
	}

	return n, nil
}

// SaveMembership persists a single membership record.
func (s *store) SaveMembership(ctx context.Context, m *network.Membership) error {
	if m == nil {
		return fmt.Errorf("persistence: save membership: nil membership")
	}

	row := membershipRow{
		BodyID:      m.BodyID,
		PeerID:      m.PeerID,
		OverlayAddr: m.OverlayAddress.String(),
		Status:      m.Status,
	}
	if m.WireGuardPublicKey != nil {
		row.WireGuardPublicKey = m.WireGuardPublicKey[:]
	}

	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("%w: marshal membership: %v", ErrCannotSave, err)
	}

	key := membershipKey(m.PeerID)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO network_state (key, value, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		key, string(data),
	)
	if err != nil {
		return fmt.Errorf("%w: save membership %s: %v", ErrCannotSave, m.PeerID, err)
	}
	return nil
}

// LoadMembership retrieves a single membership by PeerID.
func (s *store) LoadMembership(ctx context.Context, peerID network.PeerID) (*network.Membership, error) {
	key := membershipKey(peerID)
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM network_state WHERE key = ?`, key,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load membership %s: %v", ErrCannotDecode, peerID, err)
	}

	return unmarshalMembership(data)
}

// ListMemberships retrieves all persisted membership records.
func (s *store) ListMemberships(ctx context.Context) ([]*network.Membership, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT value FROM network_state WHERE key LIKE 'network:membership:%'`,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: list memberships: %v", ErrCannotDecode, err)
	}
	defer rows.Close()

	var memberships []*network.Membership
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("%w: scan membership row: %v", ErrCannotDecode, err)
		}
		m, err := unmarshalMembership(data)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate membership rows: %v", ErrCannotDecode, err)
	}

	if memberships == nil {
		memberships = []*network.Membership{}
	}
	return memberships, nil
}

// DeleteMembership removes a membership record by PeerID.
func (s *store) DeleteMembership(ctx context.Context, peerID network.PeerID) error {
	key := membershipKey(peerID)
	_, err := s.db.ExecContext(ctx, `DELETE FROM network_state WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete membership %s: %v", peerID, err)
	}
	return nil
}

// NewNetworkStore opens (or creates) a SQLite database at dbPath and returns it
// as a network.NetworkStore. This is the entry point for code that needs to
// persist network state through the standard SQLite backend.
func NewNetworkStore(dbPath string) (network.NetworkStore, error) {
	s, err := NewStore(dbPath)
	if err != nil {
		return nil, err
	}
	// The underlying *store satisfies both Store and network.NetworkStore.
	ns, ok := s.(network.NetworkStore)
	if !ok {
		s.Close()
		return nil, fmt.Errorf("persistence: store does not implement NetworkStore")
	}
	return ns, nil
}

// membershipKey returns the storage key for a given PeerID.
func membershipKey(peerID network.PeerID) string {
	return "network:membership:" + string(peerID)
}

// unmarshalMembership decodes a membership from its JSON string.
func unmarshalMembership(data string) (*network.Membership, error) {
	var row membershipRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		return nil, fmt.Errorf("%w: unmarshal membership: %v", ErrCannotDecode, err)
	}

	addr, err := netip.ParseAddr(row.OverlayAddr)
	if err != nil {
		return nil, fmt.Errorf("%w: parse overlay addr %q: %v", ErrCannotDecode, row.OverlayAddr, err)
	}

	m := &network.Membership{
		BodyID:         row.BodyID,
		PeerID:         row.PeerID,
		OverlayAddress: addr,
		Status:         row.Status,
	}
	switch len(row.WireGuardPublicKey) {
	case 0:
		// nil is valid — the Body has not yet supplied its public key.
	case 32:
		var pubKey network.WireGuardPublicKey
		copy(pubKey[:], row.WireGuardPublicKey)
		m.WireGuardPublicKey = &pubKey
	default:
		return nil, fmt.Errorf("%w: wireguard public key length %d != 32", ErrCannotDecode, len(row.WireGuardPublicKey))
	}

	return m, nil
}
