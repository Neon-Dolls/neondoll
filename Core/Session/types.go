// Package session owns Core Interaction Session lifecycle and persistence
// boundaries. Sessions are disposable working contexts; they do not own Doll
// identity or continuity-bearing state.
package session

import (
	"context"
	"errors"
	"time"
)

type ID string

// TurnID identifies one Core-owned external interaction within a Session. It
// is independent of the Session, Cognitive Run, event, and provider IDs.
type TurnID string
type OwnerKind string
type Status string

const (
	OwnerBody    OwnerKind = "body"
	OwnerGateway OwnerKind = "gateway"

	StatusActive Status = "active"
	StatusClosed Status = "closed"
)

var (
	ErrNotFound          = errors.New("session: not found")
	ErrInvalidRequest    = errors.New("session: invalid request")
	ErrInvalidTransition = errors.New("session: invalid lifecycle transition")
	ErrConversationInUse = errors.New("session: external conversation already has an active session")
)

// Owner identifies the Body or Gateway instance that owns an Interaction
// Session. InstanceID is an authenticated Body identity or a Core-assigned
// Gateway instance identity; it is never a transport address.
type Owner struct {
	Kind       OwnerKind `json:"kind"`
	InstanceID string    `json:"instance_id"`
}

// InteractionSession is Core-owned semantic working state. Model/provider
// context and continuity-bearing Doll state deliberately do not live here.
type InteractionSession struct {
	ID                      ID        `json:"session_id"`
	Owner                   Owner     `json:"owner"`
	ExternalConversationRef string    `json:"external_conversation_ref,omitempty"`
	Status                  Status    `json:"status"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type CreateRequest struct {
	Owner                   Owner
	ExternalConversationRef string
}

// ResumeRequest selects an existing active session either by its Core session
// ID or by its owning Gateway/Body and external conversation reference. Exactly
// one selector form must be supplied.
type ResumeRequest struct {
	SessionID               ID
	Owner                   Owner
	ExternalConversationRef string
}

// ActiveFilter optionally limits ListActive to one owning Body or Gateway.
// A nil filter lists all active sessions.
type ActiveFilter struct {
	Owner Owner
}

// ParticipantBinding records presence of a Core-resolved principal in an
// Interaction Session. It conveys no authority; authorization continues to
// use the principal's independently resolved classification and policy.
type ParticipantBinding struct {
	SessionID   ID        `json:"session_id"`
	PrincipalID string    `json:"principal_id"`
	JoinedAt    time.Time `json:"joined_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// ParticipantSource records the trusted external identity from which a
// participant binding was derived. It is provenance for lifecycle
// reconciliation, not a second recipient-routing authority.
type ParticipantSource struct {
	SessionID         ID     `json:"session_id"`
	GatewayInstanceID string `json:"gateway_instance_id"`
	Platform          string `json:"platform"`
	SubjectID         string `json:"subject_id"`
	PrincipalID       string `json:"principal_id"`
}

type TrustedParticipantService interface {
	ParticipantService
	BindResolvedParticipant(context.Context, ID, ParticipantSource) (*ParticipantBinding, error)
	ReconcileParticipantSources(context.Context, []ParticipantSource, []string) error
}

// ParticipantService is the durable participant-membership boundary used by
// trusted ingress and future Session event delivery.
type ParticipantService interface {
	BindParticipant(context.Context, ID, string) (*ParticipantBinding, error)
	ListParticipants(context.Context, ID) ([]*ParticipantBinding, error)
	ListParticipantSessions(context.Context, string) ([]*ParticipantBinding, error)
}

// Service is the transport-independent session host exposed to future Gateway
// integrations. Core retains lifecycle and conversation-mapping ownership.
type Service interface {
	Create(context.Context, CreateRequest) (*InteractionSession, error)
	Lookup(context.Context, ID) (*InteractionSession, error)
	Resume(context.Context, ResumeRequest) (*InteractionSession, error)
	Close(context.Context, ID) (*InteractionSession, error)
	ListActive(context.Context, *ActiveFilter) ([]*InteractionSession, error)
}

// Store is the persistence boundary consumed by Manager. The Core SQLite store
// implements it; tests and future alternate Core stores may do so as well.
type Store interface {
	CreateSession(context.Context, *InteractionSession) error
	LoadSession(context.Context, ID) (*InteractionSession, error)
	LoadActiveSessionByConversation(context.Context, Owner, string) (*InteractionSession, error)
	ResumeSession(context.Context, ID, time.Time) (*InteractionSession, error)
	CloseSession(context.Context, ID, time.Time) (*InteractionSession, error)
	ListActiveSessions(context.Context, *Owner) ([]*InteractionSession, error)
	BindSessionParticipant(context.Context, ID, string, time.Time) (*ParticipantBinding, error)
	ListSessionParticipants(context.Context, ID) ([]*ParticipantBinding, error)
	ListPrincipalSessions(context.Context, string) ([]*ParticipantBinding, error)
	BindResolvedSessionParticipant(context.Context, ID, ParticipantSource, time.Time) (*ParticipantBinding, error)
	ReconcileSessionParticipantSources(context.Context, []ParticipantSource, []string) error
}
