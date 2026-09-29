// Package body — persistent Body state.
//
// M1 separation invariant: persistent identity/membership state is kept
// separate from transient connection/endpoint state.
//
//   - BodyState   : what survives restart and is portable (identity, WG
//                   keypair, network membership).
//   - Endpoint    : runtime topology (host, port, transport, relay route)
//                   that describes how packets can currently reach Core.
//                   Endpoints are runtime data, not identity, and are never
//                   stored as part of BodyState.
//
// Source IP addresses and endpoints are NEVER treated as identity (Doll
// Network: "Implementations MUST NOT derive identity or authority from an
// overlay address" and "Source IP is not identity").

package body

import (
	"encoding/json"
	"errors"
)

// MembershipStatus is the lifecycle state of this Body's network membership.
// M1 defines the type and the lifecycle vocabulary; actual membership is
// established by M2 pairing.
type MembershipStatus string

const (
	// MembershipPending means the Body has a local record but no confirmed
	// network membership yet (i.e., before successful pairing).
	MembershipPending MembershipStatus = "pending"
	// MembershipActive means Core has enrolled this Body in a Doll Network.
	MembershipActive MembershipStatus = "active"
	// MembershipRevoked means Core has revoked this Body's membership.
	MembershipRevoked MembershipStatus = "revoked"
)

// Membership is this Body's durable record of its relationship to a Doll
// Network, once established. It is portable and survives restart. Before
// pairing it is simply empty/pending; after M2 it carries network identity.
type Membership struct {
	NetworkID string           `json:"network_id,omitempty"`
	PeerID    string           `json:"peer_id,omitempty"`
	Status    MembershipStatus `json:"status"`

	// M2 fields: persisted after successful pairing.
	BodyPeerID   string `json:"body_peer_id,omitempty"`    // Body's peer ID in the network (assigned by Core)
	BodyIPv6     string `json:"body_ipv6,omitempty"`       // Body's overlay IPv6 address (assigned by Core)
	CorePeerID   string `json:"core_peer_id,omitempty"`    // Core's peer ID in the network
	CoreWGKeyB64 string `json:"core_wg_key_b64,omitempty"` // Core's WireGuard public key (base64)
}

// NewPendingMembership returns an empty, pre-pairing membership record.
func NewPendingMembership() Membership {
	return Membership{Status: MembershipPending}
}

// CurrentMembershipVersion is the format version of the persistable membership
// envelope. Bump it (and teach UnmarshalMembership) before changing the shape.
const CurrentMembershipVersion = 1

// MembershipState is the persistable form of the Body's network membership.
// Keeping it nested under a versioned envelope lets the store evolve its file
// format without coupling to either the identity envelope or the wire shape.
type MembershipState struct {
	Version    int        `json:"version"`
	Membership Membership `json:"membership"`
}

// MarshalMembership returns the canonical JSON encoding of a MembershipState
// envelope containing `m`.
func MarshalMembership(m *Membership) (string, error) {
	env := MembershipState{
		Version:    CurrentMembershipVersion,
		Membership: *m,
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// UnmarshalMembership parses and validates a persisted membership envelope.
func UnmarshalMembership(raw string) (*Membership, error) {
	var env MembershipState
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}
	if env.Version != CurrentMembershipVersion {
		return nil, errors.New("unsupported membership version")
	}
	return &env.Membership, nil
}

// EndpointTransport enumerates the v1 packet transports (Doll Network
// Protocol §4).
type EndpointTransport string

const (
	TransportUDP EndpointTransport = "udp"
)

// EndpointType enumerates the v1 endpoint kinds: direct or relay.
type EndpointType string

const (
	EndpointDirect EndpointType = "direct"
	EndpointRelay  EndpointType = "relay"
)

// Endpoint describes how the Body can currently reach Core. This is runtime
// topology, not identity: endpoints can change, appear, and disappear without
// changing the Body, its membership, its WG keys, or its overlay address.
type Endpoint struct {
	Type      EndpointType      `json:"type"`
	Host      string            `json:"host,omitempty"`
	Port      int               `json:"port,omitempty"`
	Transport EndpointTransport `json:"transport,omitempty"`
	RelayURL  string            `json:"relay_url,omitempty"`
	RouteID   string            `json:"route_id,omitempty"`
}

// EndpointSet is the transient collection of currently-known endpoints. It is
// runtime state and is deliberately NOT part of BodyState.
type EndpointSet struct {
	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

// EmptyEndpointSet returns an endpoint set with no endpoints.
func EmptyEndpointSet() EndpointSet {
	return EndpointSet{}
}

// AppendEndpoint adds an endpoint to the set.
func (s *EndpointSet) AppendEndpoint(e Endpoint) {
	s.Endpoints = append(s.Endpoints, e)
}

// Count returns how many endpoints are present.
func (s *EndpointSet) Count() int {
	return len(s.Endpoints)
}
