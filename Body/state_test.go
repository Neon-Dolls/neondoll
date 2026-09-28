package body

import (
	"testing"
)

// TestMembershipAndEndpointSeparation: persistent membership and transient
// endpoints are distinct concepts; mutating endpoints never changes identity.
func TestMembershipAndEndpointSeparation(t *testing.T) {
	m := NewPendingMembership()
	if m.Status != MembershipPending {
		t.Errorf("fresh membership status = %q, want pending", m.Status)
	}
	// Endpoints are a separate, empty set — no identity relationship.
	es := EmptyEndpointSet()
	if es.Count() != 0 {
		t.Errorf("fresh endpoint set should be empty, got %d", es.Count())
	}

	// Mutating endpoints does not touch membership fields.
	es.AppendEndpoint(Endpoint{
		Type:      EndpointDirect,
		Host:      "203.0.113.20",
		Port:      51820,
		Transport: TransportUDP,
	})
	es.AppendEndpoint(Endpoint{
		Type:     EndpointRelay,
		RelayURL: "https://relay.example.net",
		RouteID:  "route_opaque",
	})
	if es.Count() != 2 {
		t.Errorf("expected 2 endpoints, got %d", es.Count())
	}
	// Membership is untouched by endpoint activity.
	if m.Status != MembershipPending {
		t.Errorf("endpoint mutation changed membership status")
	}
	if m.NetworkID != "" {
		t.Errorf("endpoint mutation set membership network_id")
	}
}

// TestMembershipLifecycle: status transitions are explicit.
func TestMembershipLifecycle(t *testing.T) {
	m := NewPendingMembership()
	m.Status = MembershipActive
	m.NetworkID = "net_opaque"
	m.PeerID = "peer_opaque"
	if m.Status != MembershipActive {
		t.Errorf("expected active membership")
	}
	if m.NetworkID != "net_opaque" || m.PeerID != "peer_opaque" {
		t.Errorf("membership fields not retained")
	}
	m.Status = MembershipRevoked
	if m.Status != MembershipRevoked {
		t.Errorf("expected revoked membership")
	}
}

// TestNewPendingMembershipIsEmpty: a pre-pairing membership carries no network
// or peer identity.
func TestNewPendingMembershipIsEmpty(t *testing.T) {
	m := NewPendingMembership()
	if m.NetworkID != "" || m.PeerID != "" {
		t.Errorf("pre-pairing membership should have no network identity")
	}
}
