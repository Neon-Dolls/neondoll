package peer

import (
	"errors"
	"testing"
	"time"
)

var testTime = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func testBinding(id, gateway string, assurance Assurance) Binding {
	fresh := testTime.Add(time.Hour)
	return Binding{
		SchemaVersion: 1, BindingID: "binding-" + gateway, PeerID: ID(id),
		Claim:        Claim{Namespace: "telegram", Subject: "42", DisplayName: "claimed name"},
		Evidence:     TransportEvidence{EvidenceID: "evidence-" + gateway, GatewayInstance: gateway, Transport: "gateway", AccountNamespace: "telegram", AccountSubject: "42", Method: "gateway_account", AuthenticatedAt: testTime, FreshUntil: &fresh},
		Verification: Verification{Method: "configured_binding", Scope: []string{"conversation", "authority"}, Assurance: assurance, VerifiedAt: testTime, FreshUntil: &fresh, Provenance: "core-config"},
	}
}

func TestPeerIdentityStableAcrossGatewayBindings(t *testing.T) {
	peer := Identity{SchemaVersion: 1, ID: "peer-zero", CreatedAt: testTime}
	if err := peer.Validate(); err != nil {
		t.Fatal(err)
	}
	telegram := testBinding("peer-zero", "telegram-main", AssuranceAccount)
	desktop := testBinding("peer-zero", "desktop-main", AssuranceAccount)
	if err := telegram.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := desktop.Validate(); err != nil {
		t.Fatal(err)
	}
	if telegram.PeerID != peer.ID || desktop.PeerID != peer.ID || telegram.Evidence.GatewayInstance == desktop.Evidence.GatewayInstance {
		t.Fatal("transport bindings changed stable Peer identity")
	}
}

func TestUnknownAndVerifiedResolutionRemainDistinct(t *testing.T) {
	evidence := testBinding("peer-zero", "gateway", AssuranceAccount).Evidence
	unknown := Resolution{Status: ResolutionUnknown, Evidence: evidence}
	if err := unknown.Validate(); err != nil {
		t.Fatal(err)
	}
	forged := unknown
	forged.PeerID = "peer-zero"
	if err := forged.Validate(); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("forged unknown identity: %v", err)
	}
	binding := testBinding("peer-zero", "gateway", AssuranceAccount)
	verified := Resolution{Status: ResolutionVerified, PeerID: binding.PeerID, Evidence: evidence, Binding: &binding}
	if err := verified.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAssuranceScopeAndFreshness(t *testing.T) {
	binding := testBinding("peer-zero", "gateway", AssuranceAccount)
	if !binding.AuthorizesEvidence(testTime.Add(time.Minute), "conversation", AssuranceAccount) {
		t.Fatal("fresh routine binding rejected")
	}
	if binding.AuthorizesEvidence(testTime.Add(time.Minute), "authority", AssuranceReverified) {
		t.Fatal("account evidence accepted for sensitive authority")
	}
	if binding.AuthorizesEvidence(testTime.Add(2*time.Hour), "conversation", AssuranceAccount) {
		t.Fatal("stale evidence accepted")
	}
	if err := RequireAssurance(AssuranceAccount, OperationPrimaryPeerChange); !errors.Is(err, ErrInsufficientAssurance) {
		t.Fatalf("primary change: %v", err)
	}
	if err := RequireAssurance(AssuranceReverified, OperationCredentialChange); err != nil {
		t.Fatal(err)
	}
}
