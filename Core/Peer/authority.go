package peer

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type AuthorityEffect string

const (
	EffectAllow    AuthorityEffect = "allow"
	EffectProhibit AuthorityEffect = "prohibit"
	EffectRevoke   AuthorityEffect = "revoke"
)

type IssuerRole string

const (
	IssuerPeer        IssuerRole = "peer"
	IssuerPrimaryPeer IssuerRole = "primary_peer"
)

type GrantUse string

const (
	GrantReusable GrantUse = "reusable"
	GrantOneTime  GrantUse = "one_time"
)

type AuthoritySubject struct {
	DollID string `json:"doll_id"`
	PeerID ID     `json:"peer_id,omitempty"`
}

type ActionScope struct {
	Capability string `json:"capability"`
	Operation  string `json:"operation"`
	Resource   string `json:"resource,omitempty"`
}

// AuthorityDecision is a portable historical fact. IssuerRoleAtIssue and
// Assurance are assertions whose provenance must be verified on import; they
// are not derived from an untrusted request payload.
type AuthorityDecision struct {
	SchemaVersion     int              `json:"schema_version"`
	DecisionID        string           `json:"decision_id"`
	IssuerPeerID      ID               `json:"issuer_peer_id"`
	IssuerRoleAtIssue IssuerRole       `json:"issuer_role_at_issue"`
	Subject           AuthoritySubject `json:"subject"`
	Action            ActionScope      `json:"action"`
	Effect            AuthorityEffect  `json:"effect"`
	Use               GrantUse         `json:"use,omitempty"`
	CorrelationID     string           `json:"correlation_id,omitempty"`
	RevokesDecisionID string           `json:"revokes_decision_id,omitempty"`
	IssuedAt          time.Time        `json:"issued_at"`
	ExpiresAt         *time.Time       `json:"expires_at,omitempty"`
	TieBreaker        string           `json:"tie_breaker"`
	Provenance        string           `json:"provenance"`
	Assurance         Assurance        `json:"assurance"`
}

func (d AuthorityDecision) Validate() error {
	if d.SchemaVersion != SchemaVersion || strings.TrimSpace(d.DecisionID) == "" || !validID(d.IssuerPeerID) ||
		strings.TrimSpace(d.Subject.DollID) == "" || strings.TrimSpace(d.Action.Capability) == "" ||
		strings.TrimSpace(d.Action.Operation) == "" || d.IssuedAt.IsZero() || strings.TrimSpace(d.TieBreaker) == "" ||
		strings.TrimSpace(d.Provenance) == "" || d.Assurance.rank() < AssuranceAccount.rank() ||
		(d.IssuerRoleAtIssue != IssuerPeer && d.IssuerRoleAtIssue != IssuerPrimaryPeer) {
		return ErrInvalidDecision
	}
	if d.ExpiresAt != nil && !d.ExpiresAt.After(d.IssuedAt) {
		return fmt.Errorf("%w: expiry", ErrInvalidDecision)
	}
	switch d.Effect {
	case EffectAllow:
		if d.Use != GrantReusable && d.Use != GrantOneTime {
			return ErrInvalidDecision
		}
		if d.Use == GrantOneTime && strings.TrimSpace(d.CorrelationID) == "" {
			return ErrInvalidDecision
		}
		if d.Use == GrantReusable && d.CorrelationID != "" {
			return ErrInvalidDecision
		}
		if d.RevokesDecisionID != "" {
			return ErrInvalidDecision
		}
	case EffectProhibit:
		if d.Use != "" || d.CorrelationID != "" || d.RevokesDecisionID != "" {
			return ErrInvalidDecision
		}
	case EffectRevoke:
		if d.Use != "" || d.CorrelationID != "" || d.ExpiresAt != nil || strings.TrimSpace(d.RevokesDecisionID) == "" || d.RevokesDecisionID == d.DecisionID {
			return ErrInvalidDecision
		}
	default:
		return ErrInvalidDecision
	}
	return nil
}

type AuthorityQuery struct {
	Subject       AuthoritySubject
	Action        ActionScope
	CorrelationID string
	AttemptID     string
	// RequireExplicitApproval distinguishes an approval-gated operation from
	// the normal autonomy default. Missing approval never becomes prohibition.
	RequireExplicitApproval bool
	At                      time.Time
}

type EffectiveEffect string

const (
	EffectiveAllow           EffectiveEffect = "allow"
	EffectiveProhibit        EffectiveEffect = "prohibit"
	EffectiveRequireApproval EffectiveEffect = "require_approval"
)

type AuthorityResult struct {
	Effect             EffectiveEffect      `json:"effect"`
	BasisDecisionID    string               `json:"basis_decision_id,omitempty"`
	DefaultAutonomy    bool                 `json:"default_autonomy"`
	PendingConsumption *ApprovalConsumption `json:"pending_consumption,omitempty"`
}

// Delegation is the minimum portable proof that an ordinary Peer may decide
// within an exact authority scope. Its provenance and Primary-Peer grantor
// status must be verified at the trusted import/ingress boundary.
type Delegation struct {
	SchemaVersion      int              `json:"schema_version"`
	DelegationID       string           `json:"delegation_id"`
	GrantorPeerID      ID               `json:"grantor_peer_id"`
	GrantorRoleAtIssue IssuerRole       `json:"grantor_role_at_issue"`
	GranteePeerID      ID               `json:"grantee_peer_id"`
	Subject            AuthoritySubject `json:"subject"`
	Action             ActionScope      `json:"action"`
	IssuedAt           time.Time        `json:"issued_at"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty"`
	RevokedAt          *time.Time       `json:"revoked_at,omitempty"`
	VerifiedAt         time.Time        `json:"verified_at"`
	Assurance          Assurance        `json:"assurance"`
	Provenance         string           `json:"provenance"`
}

func (d Delegation) Validate() error {
	if d.SchemaVersion != SchemaVersion || strings.TrimSpace(d.DelegationID) == "" || !validID(d.GrantorPeerID) || !validID(d.GranteePeerID) ||
		d.GrantorRoleAtIssue != IssuerPrimaryPeer || d.GrantorPeerID == d.GranteePeerID || strings.TrimSpace(d.Subject.DollID) == "" || strings.TrimSpace(d.Action.Capability) == "" ||
		strings.TrimSpace(d.Action.Operation) == "" || d.IssuedAt.IsZero() || d.VerifiedAt.IsZero() ||
		d.Assurance != AssuranceReverified || strings.TrimSpace(d.Provenance) == "" {
		return ErrInvalidDecision
	}
	if d.ExpiresAt != nil && !d.ExpiresAt.After(d.IssuedAt) || d.RevokedAt != nil && d.RevokedAt.Before(d.IssuedAt) {
		return fmt.Errorf("%w: delegation lifetime", ErrInvalidDecision)
	}
	return nil
}

// ApprovalConsumption is the portable, append-only claim of a one-time
// approval. It must be durably committed before the correlated execution
// attempt starts. Its presence prevents every later attempt, including a
// retry with the same AttemptID, from consuming the approval again.
type ApprovalConsumption struct {
	SchemaVersion int       `json:"schema_version"`
	ConsumptionID string    `json:"consumption_id"`
	DecisionID    string    `json:"decision_id"`
	CorrelationID string    `json:"correlation_id"`
	AttemptID     string    `json:"attempt_id"`
	ConsumedAt    time.Time `json:"consumed_at"`
	Provenance    string    `json:"provenance"`
}

func (c ApprovalConsumption) Validate() error {
	if c.SchemaVersion != SchemaVersion || strings.TrimSpace(c.ConsumptionID) == "" || strings.TrimSpace(c.DecisionID) == "" ||
		strings.TrimSpace(c.CorrelationID) == "" || strings.TrimSpace(c.AttemptID) == "" || c.ConsumedAt.IsZero() || strings.TrimSpace(c.Provenance) == "" {
		return ErrInvalidDecision
	}
	return nil
}

// ReconcileAuthority replays portable history deterministically. It does not
// mutate or discard expired or revoked records.
func ReconcileAuthority(records []AuthorityDecision, delegations []Delegation, consumptions []ApprovalConsumption, query AuthorityQuery) (AuthorityResult, error) {
	if strings.TrimSpace(query.Subject.DollID) == "" || strings.TrimSpace(query.Action.Capability) == "" || strings.TrimSpace(query.Action.Operation) == "" || query.At.IsZero() {
		return AuthorityResult{}, ErrInvalidDecision
	}
	ordered := append([]AuthorityDecision(nil), records...)
	for _, record := range ordered {
		if err := record.Validate(); err != nil {
			return AuthorityResult{}, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].IssuedAt.Equal(ordered[j].IssuedAt) {
			return ordered[i].IssuedAt.Before(ordered[j].IssuedAt)
		}
		if ordered[i].TieBreaker != ordered[j].TieBreaker {
			return ordered[i].TieBreaker < ordered[j].TieBreaker
		}
		return ordered[i].DecisionID < ordered[j].DecisionID
	})
	if err := validateDelegations(delegations); err != nil {
		return AuthorityResult{}, err
	}
	consumed, err := validateConsumptions(consumptions, ordered, query.At)
	if err != nil {
		return AuthorityResult{}, err
	}
	byID := map[string]AuthorityDecision{}
	revoked := map[string]bool{}
	for _, record := range ordered {
		if _, duplicate := byID[record.DecisionID]; duplicate {
			return AuthorityResult{}, fmt.Errorf("%w: duplicate decision", ErrInvalidDecision)
		}
		if record.Effect == EffectRevoke {
			target, ok := byID[record.RevokesDecisionID]
			if !ok || target.Subject != record.Subject || target.Action != record.Action {
				return AuthorityResult{}, fmt.Errorf("%w: invalid revocation target", ErrInvalidDecision)
			}
			if record.IssuerRoleAtIssue != IssuerPrimaryPeer && (target.IssuerRoleAtIssue == IssuerPrimaryPeer || target.IssuerPeerID != record.IssuerPeerID) {
				return AuthorityResult{}, fmt.Errorf("%w: unauthorized revocation", ErrInvalidDecision)
			}
			if !record.IssuedAt.After(query.At) && (record.IssuerRoleAtIssue == IssuerPrimaryPeer || hasDelegation(record, delegations, query.At)) {
				revoked[target.DecisionID] = true
			}
		}
		byID[record.DecisionID] = record
	}
	var ordinary, primary *AuthorityDecision
	for index := range ordered {
		record := &ordered[index]
		if record.Effect == EffectRevoke || revoked[record.DecisionID] || consumed[record.DecisionID] || !applicable(*record, query) {
			continue
		}
		if record.IssuerRoleAtIssue == IssuerPrimaryPeer {
			primary = record
		} else if hasDelegation(*record, delegations, query.At) {
			ordinary = record
		}
	}
	selected := primary
	if selected == nil {
		selected = ordinary
	}
	if selected == nil {
		if query.RequireExplicitApproval {
			return AuthorityResult{Effect: EffectiveRequireApproval}, nil
		}
		return AuthorityResult{Effect: EffectiveAllow, DefaultAutonomy: true}, nil
	}
	if selected.Effect == EffectProhibit {
		return AuthorityResult{Effect: EffectiveProhibit, BasisDecisionID: selected.DecisionID}, nil
	}
	if selected.Use == GrantOneTime {
		if strings.TrimSpace(query.AttemptID) == "" {
			return AuthorityResult{}, fmt.Errorf("%w: one-time approval requires attempt ID", ErrInvalidDecision)
		}
		consumption := ApprovalConsumption{SchemaVersion: SchemaVersion, ConsumptionID: "consume:" + selected.DecisionID + ":" + query.AttemptID, DecisionID: selected.DecisionID, CorrelationID: query.CorrelationID, AttemptID: query.AttemptID, ConsumedAt: query.At, Provenance: "core:one-time-consumption"}
		return AuthorityResult{Effect: EffectiveAllow, BasisDecisionID: selected.DecisionID, PendingConsumption: &consumption}, nil
	}
	return AuthorityResult{Effect: EffectiveAllow, BasisDecisionID: selected.DecisionID}, nil
}

func validateDelegations(delegations []Delegation) error {
	seen := map[string]bool{}
	for _, delegation := range delegations {
		if err := delegation.Validate(); err != nil || seen[delegation.DelegationID] {
			return ErrInvalidDecision
		}
		seen[delegation.DelegationID] = true
	}
	return nil
}

func hasDelegation(record AuthorityDecision, delegations []Delegation, at time.Time) bool {
	for _, delegation := range delegations {
		if delegation.GranteePeerID == record.IssuerPeerID && delegation.Subject == record.Subject && delegation.Action == record.Action &&
			!record.IssuedAt.Before(delegation.IssuedAt) && !record.IssuedAt.After(at) && !delegation.VerifiedAt.After(at) &&
			(delegation.ExpiresAt == nil || delegation.ExpiresAt.After(at)) && (delegation.RevokedAt == nil || delegation.RevokedAt.After(at)) {
			return true
		}
	}
	return false
}

func validateConsumptions(consumptions []ApprovalConsumption, decisions []AuthorityDecision, at time.Time) (map[string]bool, error) {
	byDecision := map[string]AuthorityDecision{}
	for _, decision := range decisions {
		byDecision[decision.DecisionID] = decision
	}
	consumed := map[string]bool{}
	consumedDecision := map[string]bool{}
	ids := map[string]bool{}
	for _, consumption := range consumptions {
		decision, ok := byDecision[consumption.DecisionID]
		if err := consumption.Validate(); err != nil || !ok || decision.Effect != EffectAllow || decision.Use != GrantOneTime ||
			consumption.CorrelationID != decision.CorrelationID || consumption.ConsumedAt.Before(decision.IssuedAt) ||
			decision.ExpiresAt != nil && !decision.ExpiresAt.After(consumption.ConsumedAt) || ids[consumption.ConsumptionID] || consumedDecision[consumption.DecisionID] {
			return nil, ErrInvalidDecision
		}
		ids[consumption.ConsumptionID] = true
		consumedDecision[consumption.DecisionID] = true
		if !consumption.ConsumedAt.After(at) {
			consumed[consumption.DecisionID] = true
		}
	}
	return consumed, nil
}

func applicable(record AuthorityDecision, query AuthorityQuery) bool {
	if record.Subject != query.Subject || record.Action != query.Action || record.IssuedAt.After(query.At) || (record.ExpiresAt != nil && !record.ExpiresAt.After(query.At)) {
		return false
	}
	return record.Effect != EffectAllow || record.Use != GrantOneTime || record.CorrelationID == query.CorrelationID
}
