// Package peer defines portable Core 6 Peer identity, relationship, and
// authority contracts. It is not wired into Core 5 authorization.
package peer

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

type ID string

var (
	ErrInvalidIdentity       = errors.New("peer: invalid identity")
	ErrInvalidRelationship   = errors.New("peer: invalid relationship")
	ErrInvalidDecision       = errors.New("peer: invalid authority decision")
	ErrInsufficientAssurance = errors.New("peer: insufficient assurance")
)

// Identity is a stable Core-level identity. It is independent of accounts,
// transports, sessions, Gateway instances, and Body connections.
type Identity struct {
	SchemaVersion int       `json:"schema_version"`
	ID            ID        `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
}

func (i Identity) Validate() error {
	if i.SchemaVersion != SchemaVersion || !validID(i.ID) || i.CreatedAt.IsZero() {
		return ErrInvalidIdentity
	}
	return nil
}

// Claim is asserted identity information. It is evidence input only and does
// not identify or authorize a Peer.
type Claim struct {
	Namespace   string `json:"namespace"`
	Subject     string `json:"subject"`
	DisplayName string `json:"display_name,omitempty"`
}

// TransportEvidence records what an authenticated boundary actually proved.
// Account or device authentication does not prove physical human authorship.
type TransportEvidence struct {
	EvidenceID       string     `json:"evidence_id"`
	GatewayInstance  string     `json:"gateway_instance,omitempty"`
	Transport        string     `json:"transport"`
	AccountNamespace string     `json:"account_namespace,omitempty"`
	AccountSubject   string     `json:"account_subject,omitempty"`
	DeviceID         string     `json:"device_id,omitempty"`
	Method           string     `json:"method"`
	AuthenticatedAt  time.Time  `json:"authenticated_at"`
	FreshUntil       *time.Time `json:"fresh_until,omitempty"`
}

type Assurance string

const (
	AssuranceNone       Assurance = "none"
	AssuranceAccount    Assurance = "authenticated_account"
	AssuranceReverified Assurance = "reverified"
)

func (a Assurance) rank() int {
	switch a {
	case AssuranceNone:
		return 0
	case AssuranceAccount:
		return 1
	case AssuranceReverified:
		return 2
	default:
		return -1
	}
}

// Verification describes a Core-owned binding decision and its limits.
type Verification struct {
	Method     string     `json:"method"`
	Scope      []string   `json:"scope"`
	Assurance  Assurance  `json:"assurance"`
	VerifiedAt time.Time  `json:"verified_at"`
	FreshUntil *time.Time `json:"fresh_until,omitempty"`
	Provenance string     `json:"provenance"`
}

// Binding associates authenticated evidence with a recognized Peer. Claims
// remain separate so display names and payload roles cannot become authority.
type Binding struct {
	SchemaVersion int               `json:"schema_version"`
	BindingID     string            `json:"binding_id"`
	PeerID        ID                `json:"peer_id"`
	Claim         Claim             `json:"claim"`
	Evidence      TransportEvidence `json:"evidence"`
	Verification  Verification      `json:"verification"`
	RevokedAt     *time.Time        `json:"revoked_at,omitempty"`
}

func (b Binding) Validate() error {
	if b.SchemaVersion != SchemaVersion || strings.TrimSpace(b.BindingID) == "" || !validID(b.PeerID) ||
		strings.TrimSpace(b.Claim.Namespace) == "" || strings.TrimSpace(b.Claim.Subject) == "" ||
		strings.TrimSpace(b.Evidence.EvidenceID) == "" || strings.TrimSpace(b.Evidence.Transport) == "" ||
		strings.TrimSpace(b.Evidence.Method) == "" || b.Evidence.AuthenticatedAt.IsZero() ||
		strings.TrimSpace(b.Verification.Method) == "" || strings.TrimSpace(b.Verification.Provenance) == "" ||
		b.Verification.VerifiedAt.IsZero() || b.Verification.Assurance.rank() < 0 {
		return ErrInvalidIdentity
	}
	if b.Evidence.FreshUntil != nil && !b.Evidence.FreshUntil.After(b.Evidence.AuthenticatedAt) {
		return fmt.Errorf("%w: evidence freshness", ErrInvalidIdentity)
	}
	if b.Verification.FreshUntil != nil && !b.Verification.FreshUntil.After(b.Verification.VerifiedAt) {
		return fmt.Errorf("%w: verification freshness", ErrInvalidIdentity)
	}
	if len(b.Verification.Scope) == 0 {
		return fmt.Errorf("%w: verification scope", ErrInvalidIdentity)
	}
	for _, scope := range b.Verification.Scope {
		if strings.TrimSpace(scope) == "" {
			return fmt.Errorf("%w: empty verification scope", ErrInvalidIdentity)
		}
	}
	return nil
}

// AuthorizesEvidence reports whether a valid, unrevoked binding is fresh,
// scoped, and strong enough for an operation. It does not authorize the
// operation itself.
func (b Binding) AuthorizesEvidence(at time.Time, scope string, required Assurance) bool {
	if b.Validate() != nil || at.IsZero() || strings.TrimSpace(scope) == "" || required.rank() < 0 || b.RevokedAt != nil && !b.RevokedAt.After(at) ||
		b.Evidence.AuthenticatedAt.After(at) || b.Verification.VerifiedAt.After(at) ||
		b.Evidence.FreshUntil != nil && !b.Evidence.FreshUntil.After(at) || b.Verification.FreshUntil != nil && !b.Verification.FreshUntil.After(at) ||
		b.Verification.Assurance.rank() < required.rank() {
		return false
	}
	for _, candidate := range b.Verification.Scope {
		if candidate == scope {
			return true
		}
	}
	return false
}

type ResolutionStatus string

const (
	ResolutionUnknown  ResolutionStatus = "unknown"
	ResolutionVerified ResolutionStatus = "verified"
)

// Resolution keeps unknown evidence unknown. A verified resolution must carry
// a complete binding; an unknown resolution must not carry a Peer ID.
type Resolution struct {
	Status   ResolutionStatus  `json:"status"`
	PeerID   ID                `json:"peer_id,omitempty"`
	Evidence TransportEvidence `json:"evidence"`
	Binding  *Binding          `json:"binding,omitempty"`
}

func (r Resolution) Validate() error {
	switch r.Status {
	case ResolutionUnknown:
		if r.PeerID != "" || r.Binding != nil {
			return ErrInvalidIdentity
		}
	case ResolutionVerified:
		if r.Binding == nil || r.PeerID != r.Binding.PeerID || r.Evidence.EvidenceID != r.Binding.Evidence.EvidenceID {
			return ErrInvalidIdentity
		}
		return r.Binding.Validate()
	default:
		return ErrInvalidIdentity
	}
	return nil
}

type OperationClass string

const (
	OperationRoutineConversation OperationClass = "routine_conversation"
	OperationPrimaryPeerChange   OperationClass = "primary_peer_change"
	OperationCredentialChange    OperationClass = "credential_change"
)

func RequiredAssurance(class OperationClass) (Assurance, error) {
	switch class {
	case OperationRoutineConversation:
		return AssuranceAccount, nil
	case OperationPrimaryPeerChange, OperationCredentialChange:
		return AssuranceReverified, nil
	default:
		return "", ErrInvalidIdentity
	}
}

func RequireAssurance(actual Assurance, class OperationClass) error {
	required, err := RequiredAssurance(class)
	if err != nil {
		return err
	}
	if actual.rank() < required.rank() {
		return ErrInsufficientAssurance
	}
	return nil
}

func validID(id ID) bool { return strings.TrimSpace(string(id)) != "" }
