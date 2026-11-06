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
	At            time.Time
}

type EffectiveEffect string

const (
	EffectiveAllow    EffectiveEffect = "allow"
	EffectiveProhibit EffectiveEffect = "prohibit"
)

type AuthorityResult struct {
	Effect          EffectiveEffect `json:"effect"`
	BasisDecisionID string          `json:"basis_decision_id,omitempty"`
	DefaultAutonomy bool            `json:"default_autonomy"`
}

// ReconcileAuthority replays portable history deterministically. It does not
// mutate or discard expired or revoked records.
func ReconcileAuthority(records []AuthorityDecision, query AuthorityQuery) (AuthorityResult, error) {
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
			if !record.IssuedAt.After(query.At) {
				revoked[target.DecisionID] = true
			}
		}
		byID[record.DecisionID] = record
	}
	var ordinary, primary *AuthorityDecision
	for index := range ordered {
		record := &ordered[index]
		if record.Effect == EffectRevoke || revoked[record.DecisionID] || !applicable(*record, query) {
			continue
		}
		if record.IssuerRoleAtIssue == IssuerPrimaryPeer {
			primary = record
		} else {
			ordinary = record
		}
	}
	selected := primary
	if selected == nil {
		selected = ordinary
	}
	if selected == nil {
		return AuthorityResult{Effect: EffectiveAllow, DefaultAutonomy: true}, nil
	}
	if selected.Effect == EffectProhibit {
		return AuthorityResult{Effect: EffectiveProhibit, BasisDecisionID: selected.DecisionID}, nil
	}
	return AuthorityResult{Effect: EffectiveAllow, BasisDecisionID: selected.DecisionID}, nil
}

func applicable(record AuthorityDecision, query AuthorityQuery) bool {
	if record.Subject != query.Subject || record.Action != query.Action || record.IssuedAt.After(query.At) || (record.ExpiresAt != nil && !record.ExpiresAt.After(query.At)) {
		return false
	}
	return record.Effect != EffectAllow || record.Use != GrantOneTime || record.CorrelationID == query.CorrelationID
}
