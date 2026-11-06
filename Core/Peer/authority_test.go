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

func delegation(id string, grantee ID) Delegation {
	return Delegation{SchemaVersion: 1, DelegationID: id, GrantorPeerID: "primary", GrantorRoleAtIssue: IssuerPrimaryPeer, GranteePeerID: grantee,
		Subject: AuthoritySubject{DollID: "doll-1"}, Action: ActionScope{Capability: "mail", Operation: "send", Resource: "peer-a"},
		IssuedAt: testTime.Add(-time.Hour), VerifiedAt: testTime.Add(-time.Hour), Assurance: AssuranceReverified, Provenance: "primary-peer-signed"}
}

func TestAuthorityDefaultsAllowAndPrimaryPrecedence(t *testing.T) {
	result, err := ReconcileAuthority(nil, nil, nil, query(testTime))
	if err != nil || result.Effect != EffectiveAllow || !result.DefaultAutonomy {
		t.Fatalf("default: %+v %v", result, err)
	}
	primary := decision("primary-allow", "primary", IssuerPrimaryPeer, EffectAllow, testTime)
	primary.Use = GrantReusable
	ordinary := decision("peer-deny", "peer", IssuerPeer, EffectProhibit, testTime.Add(time.Hour))
	result, err = ReconcileAuthority([]AuthorityDecision{ordinary, primary}, nil, nil, query(testTime.Add(2*time.Hour)))
	if err != nil || result.Effect != EffectiveAllow || result.BasisDecisionID != primary.DecisionID {
		t.Fatalf("primary allow: %+v %v", result, err)
	}
	primary.Effect, primary.Use, primary.DecisionID = EffectProhibit, "", "primary-deny"
	ordinary.Effect, ordinary.Use, ordinary.DecisionID = EffectAllow, GrantReusable, "peer-allow"
	result, err = ReconcileAuthority([]AuthorityDecision{ordinary, primary}, nil, nil, query(testTime.Add(2*time.Hour)))
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
		result, err := ReconcileAuthority(records, nil, nil, query(testTime.Add(time.Minute)))
		if err != nil || result.BasisDecisionID != "b" || result.Effect != EffectiveProhibit {
			t.Fatalf("tie: %+v %v", result, err)
		}
	}
	later := allow
	later.DecisionID, later.TieBreaker, later.IssuedAt = "later", "later", testTime.Add(time.Hour)
	result, _ := ReconcileAuthority([]AuthorityDecision{deny, later}, nil, nil, query(testTime.Add(2*time.Hour)))
	if result.BasisDecisionID != "later" || result.Effect != EffectiveAllow {
		t.Fatalf("later primary: %+v", result)
	}
}

func TestRevocationExpiryAndOneTimeApproval(t *testing.T) {
	grant := decision("grant", "peer", IssuerPeer, EffectAllow, testTime)
	grant.Use = GrantReusable
	expires := testTime.Add(time.Hour)
	grant.ExpiresAt = &expires
	delegations := []Delegation{delegation("delegate-peer", "peer")}
	result, _ := ReconcileAuthority([]AuthorityDecision{grant}, delegations, nil, query(testTime.Add(2*time.Hour)))
	if !result.DefaultAutonomy || result.BasisDecisionID != "" {
		t.Fatalf("expired history authorized: %+v", result)
	}
	revoke := decision("revoke", "peer", IssuerPeer, EffectRevoke, testTime.Add(30*time.Minute))
	revoke.RevokesDecisionID = grant.DecisionID
	result, err := ReconcileAuthority([]AuthorityDecision{revoke, grant}, delegations, nil, query(testTime.Add(45*time.Minute)))
	if err != nil || !result.DefaultAutonomy {
		t.Fatalf("revocation: %+v %v", result, err)
	}
	one := decision("once", "peer", IssuerPeer, EffectAllow, testTime)
	one.Use, one.CorrelationID = GrantOneTime, "request-1"
	q := query(testTime.Add(time.Minute))
	q.CorrelationID = "request-1"
	q.AttemptID, q.RequireExplicitApproval = "attempt-1", true
	mismatched := q
	mismatched.CorrelationID = "request-2"
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, delegations, nil, mismatched)
	if result.Effect != EffectiveRequireApproval {
		t.Fatalf("mismatched correlation authorized: %+v", result)
	}
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, delegations, nil, q)
	if result.BasisDecisionID != "once" || result.PendingConsumption == nil {
		t.Fatalf("one-time match: %+v", result)
	}
	consumption := *result.PendingConsumption
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, delegations, []ApprovalConsumption{consumption}, q)
	if result.Effect != EffectiveRequireApproval || result.BasisDecisionID != "" {
		t.Fatalf("one-time reuse authorized: %+v", result)
	}
	q.AttemptID = "attempt-retry"
	result, _ = ReconcileAuthority([]AuthorityDecision{one}, delegations, []ApprovalConsumption{consumption}, q)
	if result.Effect != EffectiveRequireApproval {
		t.Fatalf("retry consumed approval: %+v", result)
	}
}

func TestOrdinaryPeerCannotRevokePrimaryDecision(t *testing.T) {
	primary := decision("primary", "primary", IssuerPrimaryPeer, EffectProhibit, testTime)
	revoke := decision("revoke", "peer", IssuerPeer, EffectRevoke, testTime.Add(time.Minute))
	revoke.RevokesDecisionID = primary.DecisionID
	if _, err := ReconcileAuthority([]AuthorityDecision{primary, revoke}, []Delegation{delegation("delegate-peer", "peer")}, nil, query(testTime.Add(time.Hour))); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("ordinary revocation: %v", err)
	}
}

func TestOrdinaryPeerRequiresApplicableVerifiedDelegation(t *testing.T) {
	denial := decision("ordinary-denial", "peer", IssuerPeer, EffectProhibit, testTime)
	q := query(testTime.Add(time.Minute))
	result, err := ReconcileAuthority([]AuthorityDecision{denial}, nil, nil, q)
	if err != nil || result.Effect != EffectiveAllow || !result.DefaultAutonomy {
		t.Fatalf("undelegated Peer controlled authority: %+v %v", result, err)
	}
	approval := decision("ordinary-approval", "peer", IssuerPeer, EffectAllow, testTime)
	approval.Use = GrantReusable
	approvalQuery := q
	approvalQuery.RequireExplicitApproval = true
	result, err = ReconcileAuthority([]AuthorityDecision{approval}, nil, nil, approvalQuery)
	if err != nil || result.Effect != EffectiveRequireApproval {
		t.Fatalf("undelegated Peer granted approval: %+v %v", result, err)
	}
	valid := delegation("delegation", "peer")
	result, err = ReconcileAuthority([]AuthorityDecision{denial}, []Delegation{valid}, nil, q)
	if err != nil || result.Effect != EffectiveProhibit || result.BasisDecisionID != denial.DecisionID {
		t.Fatalf("delegated decision ignored: %+v %v", result, err)
	}
	invalidGrantor := valid
	invalidGrantor.DelegationID, invalidGrantor.GrantorRoleAtIssue = "ordinary-grantor", IssuerPeer
	if _, err := ReconcileAuthority([]AuthorityDecision{denial}, []Delegation{invalidGrantor}, nil, q); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("ordinary Peer created delegation: %v", err)
	}
	wrongScope := valid
	wrongScope.DelegationID, wrongScope.Action.Resource = "wrong-scope", "peer-b"
	result, err = ReconcileAuthority([]AuthorityDecision{denial}, []Delegation{wrongScope}, nil, q)
	if err != nil || !result.DefaultAutonomy {
		t.Fatalf("wrong scope controlled authority: %+v %v", result, err)
	}
	revoked := valid
	revoked.DelegationID = "revoked"
	revokedAt := testTime.Add(30 * time.Second)
	revoked.RevokedAt = &revokedAt
	result, err = ReconcileAuthority([]AuthorityDecision{denial}, []Delegation{revoked}, nil, q)
	if err != nil || !result.DefaultAutonomy {
		t.Fatalf("revoked delegation controlled authority: %+v %v", result, err)
	}
}

func TestOneTimeConsumptionSurvivesPortableHistoryRestore(t *testing.T) {
	one := decision("once-portable", "peer", IssuerPeer, EffectAllow, testTime)
	one.Use, one.CorrelationID = GrantOneTime, "correlation-1"
	q := query(testTime.Add(time.Minute))
	q.CorrelationID, q.AttemptID, q.RequireExplicitApproval = "correlation-1", "attempt-1", true
	delegations := []Delegation{delegation("delegate-peer", "peer")}
	first, err := ReconcileAuthority([]AuthorityDecision{one}, delegations, nil, q)
	if err != nil || first.PendingConsumption == nil {
		t.Fatalf("first use: %+v %v", first, err)
	}
	if err := first.PendingConsumption.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first.PendingConsumption)
	if err != nil {
		t.Fatal(err)
	}
	var restored ApprovalConsumption
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	afterRestart, err := ReconcileAuthority([]AuthorityDecision{one}, delegations, []ApprovalConsumption{restored}, q)
	if err != nil || afterRestart.Effect != EffectiveRequireApproval || afterRestart.PendingConsumption != nil {
		t.Fatalf("restored use authorized again: %+v %v", afterRestart, err)
	}
}

func TestShuffledHistoryProducesIdenticalState(t *testing.T) {
	first := decision("first", "primary", IssuerPrimaryPeer, EffectProhibit, testTime)
	second := decision("second", "primary", IssuerPrimaryPeer, EffectAllow, testTime.Add(time.Hour))
	second.Use = GrantReusable
	ordinary := decision("ordinary", "peer", IssuerPeer, EffectProhibit, testTime.Add(2*time.Hour))
	records := []AuthorityDecision{first, second, ordinary}
	want, err := ReconcileAuthority(records, nil, nil, query(testTime.Add(3*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(0); seed < 20; seed++ {
		shuffled := append([]AuthorityDecision(nil), records...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, err := ReconcileAuthority(shuffled, nil, nil, query(testTime.Add(3*time.Hour)))
		if err != nil || got != want {
			t.Fatalf("seed %d: %+v %v want %+v", seed, got, err, want)
		}
	}
}
