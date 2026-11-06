package peer

import (
	"encoding/json"
	"errors"
	"math/rand"
	"testing"
	"time"
)

func decision(id string, issuer ID, role IssuerRole, effect AuthorityEffect, at time.Time) AuthorityDecision {
	return AuthorityDecision{SchemaVersion: 1, DecisionID: id, IssuerPeerID: issuer, IssuerRoleAtIssue: role,
		Subject: AuthoritySubject{DollID: "doll-1"}, Action: ActionScope{Capability: "mail", Operation: "send", Resource: "peer-a"},
		Effect: effect, IssuedAt: at, TieBreaker: id, Provenance: "authenticated-decision", Assurance: AssuranceAccount}
}

func TestAuthorityDecisionPortableSerialization(t *testing.T) {
	original := decision("portable", "primary", IssuerPrimaryPeer, EffectAllow, testTime)
	original.Use = GrantReusable
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AuthorityDecision
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if decoded.DecisionID != original.DecisionID || decoded.Subject != original.Subject || decoded.Action != original.Action {
		t.Fatalf("round trip: %+v", decoded)
	}
}

func query(at time.Time) AuthorityQuery {
	return AuthorityQuery{Subject: AuthoritySubject{DollID: "doll-1"}, Action: ActionScope{Capability: "mail", Operation: "send", Resource: "peer-a"}, At: at}
}

func TestAuthorityDefaultsAllowAndPrimaryPrecedence(t *testing.T) {
	result, err := ReconcileAuthority(nil, query(testTime))
	if err != nil || result.Effect != EffectiveAllow || !result.DefaultAutonomy {
		t.Fatalf("default: %+v %v", result, err)
	}
	primary := decision("primary-allow", "primary", IssuerPrimaryPeer, EffectAllow, testTime)
	primary.Use = GrantReusable
	ordinary := decision("peer-deny", "peer", IssuerPeer, EffectProhibit, testTime.Add(time.Hour))
	result, err = ReconcileAuthority([]AuthorityDecision{ordinary, primary}, query(testTime.Add(2*time.Hour)))
	if err != nil || result.Effect != EffectiveAllow || result.BasisDecisionID != primary.DecisionID {
		t.Fatalf("primary allow: %+v %v", result, err)
	}
	primary.Effect, primary.Use, primary.DecisionID = EffectProhibit, "", "primary-deny"
	ordinary.Effect, ordinary.Use, ordinary.DecisionID = EffectAllow, GrantReusable, "peer-allow"
	result, err = ReconcileAuthority([]AuthorityDecision{ordinary, primary}, query(testTime.Add(2*time.Hour)))
	if err != nil || result.Effect != EffectiveProhibit || result.BasisDecisionID != primary.DecisionID {
		t.Fatalf("primary prohibit: %+v %v", result, err)
	}
}

func TestLaterPrimaryAndDeterministicEqualTimestamp(t *testing.T) {
	allow := decision("a", "primary", IssuerPrimaryPeer, EffectAllow, testTime)
	allow.Use = GrantReusable
	allow.TieBreaker = "a"
	deny := decision("b", "primary", IssuerPrimaryPeer, EffectProhibit, testTime)
	deny.TieBreaker = "b"
	for _, records := range [][]AuthorityDecision{{allow, deny}, {deny, allow}} {
		result, err := ReconcileAuthority(records, query(testTime.Add(time.Minute)))
		if err != nil || result.BasisDecisionID != "b" || result.Effect != EffectiveProhibit {
			t.Fatalf("tie: %+v %v", result, err)
		}
	}
	later := allow
	later.DecisionID, later.TieBreaker, later.IssuedAt = "later", "later", testTime.Add(time.Hour)
	result, _ := ReconcileAuthority([]AuthorityDecision{deny, later}, query(testTime.Add(2*time.Hour)))
	if result.BasisDecisionID != "later" || result.Effect != EffectiveAllow {
		t.Fatalf("later primary: %+v", result)
	}
}

func TestRevocationExpiryAndOneTimeApproval(t *testing.T) {
	grant := decision("grant", "peer", IssuerPeer, EffectAllow, testTime)
	grant.Use = GrantReusable
	expires := testTime.Add(time.Hour)
	grant.ExpiresAt = &expires
	result, _ := ReconcileAuthority([]AuthorityDecision{grant}, query(testTime.Add(2*time.Hour)))
	if !result.DefaultAutonomy || result.BasisDecisionID != "" {
		t.Fatalf("expired history authorized: %+v", result)
	}
	revoke := decision("revoke", "peer", IssuerPeer, EffectRevoke, testTime.Add(30*time.Minute))
	revoke.RevokesDecisionID = grant.DecisionID
	result, err := ReconcileAuthority([]AuthorityDecision{revoke, grant}, query(testTime.Add(45*time.Minute)))
	if err != nil || !result.DefaultAutonomy {
		t.Fatalf("revocation: %+v %v", result, err)
	}
	one := decision("once", "peer", IssuerPeer, EffectAllow, testTime)
	one.Use, one.CorrelationID = GrantOneTime, "request-1"
	q := query(testTime.Add(time.Minute))
	q.CorrelationID = "request-1"
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, q)
	if result.BasisDecisionID != "once" {
		t.Fatalf("one-time match: %+v", result)
	}
	q.CorrelationID = "request-2"
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, q)
	if !result.DefaultAutonomy || result.BasisDecisionID != "" {
		t.Fatalf("one-time reused: %+v", result)
	}
}

func TestOrdinaryPeerCannotRevokePrimaryDecision(t *testing.T) {
	primary := decision("primary", "primary", IssuerPrimaryPeer, EffectProhibit, testTime)
	revoke := decision("revoke", "peer", IssuerPeer, EffectRevoke, testTime.Add(time.Minute))
	revoke.RevokesDecisionID = primary.DecisionID
	if _, err := ReconcileAuthority([]AuthorityDecision{primary, revoke}, query(testTime.Add(time.Hour))); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("ordinary revocation: %v", err)
	}
}

func TestShuffledHistoryProducesIdenticalState(t *testing.T) {
	first := decision("first", "primary", IssuerPrimaryPeer, EffectProhibit, testTime)
	second := decision("second", "primary", IssuerPrimaryPeer, EffectAllow, testTime.Add(time.Hour))
	second.Use = GrantReusable
	ordinary := decision("ordinary", "peer", IssuerPeer, EffectProhibit, testTime.Add(2*time.Hour))
	records := []AuthorityDecision{first, second, ordinary}
	want, err := ReconcileAuthority(records, query(testTime.Add(3*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(0); seed < 20; seed++ {
		shuffled := append([]AuthorityDecision(nil), records...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, err := ReconcileAuthority(shuffled, query(testTime.Add(3*time.Hour)))
		if err != nil || got != want {
			t.Fatalf("seed %d: %+v %v want %+v", seed, got, err, want)
		}
	}
}
