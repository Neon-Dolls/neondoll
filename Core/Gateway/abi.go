// Package gateway hosts Core-managed Gateway instances and defines their
// transport-neutral ABI. Gateway authentication is installation/runtime
// ownership by Core; event payload fields never select an instance identity.
package gateway

import (
	"context"
	"errors"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

const (
	LegacyABIVersion     = 0
	StructuredABIVersion = 1
)

var (
	ErrInvalidAdapter    = errors.New("gateway: invalid adapter")
	ErrAlreadyRegistered = errors.New("gateway: instance already registered")
	ErrUnavailable       = errors.New("gateway: instance unavailable")
	ErrUnsupportedABI    = errors.New("gateway: structured Session events unsupported")
	ErrUnsupportedOwner  = errors.New("gateway: unsupported Session owner")
	ErrHostNotConfigured = errors.New("gateway: host not configured")
)

// VerifiedSender is produced by a platform adapter after platform-level
// verification. Role, principal, and authority claims remain untrusted.
type VerifiedSender struct {
	Platform  string            `json:"platform"`
	SubjectID string            `json:"subject_id"`
	Claims    map[string]string `json:"claims,omitempty"`
}

// OutboundEnvelope is the versioned ABI object passed to Gateway adapters.
// DeliveryID remains stable across retry and is the adapter deduplication key.
type OutboundEnvelope struct {
	ABIVersion int                   `json:"abi_version"`
	Delivery   sessionevent.Delivery `json:"delivery"`
}

// Ingress is bound by Host to one configured Gateway instance. Adapters never
// pass their own instance ID or claim another Gateway's Session ownership.
type Ingress interface {
	HandleLegacy(context.Context, session.ID, VerifiedSender, *events.Event) (*events.Event, error)
	HandleSessionEvent(context.Context, VerifiedSender, sessionevent.Event) (*permissionrequest.Request, error)
}

// Adapter is the Core-managed Gateway ABI. Existing text-only Gateways use
// LegacyABIVersion and retain HandleLegacy support. Structured outbound and
// decision events require StructuredABIVersion.
type Adapter interface {
	InstanceID() externalidentity.GatewayInstanceID
	ABIVersion() int
	Start(context.Context, Ingress) error
	Deliver(context.Context, OutboundEnvelope) error
	Stop(context.Context) error
}

type boundIngress struct {
	version int
	ingress *interaction.GatewayIngress
	replay  ReplayFunc
}

func (b *boundIngress) HandleLegacy(ctx context.Context, sessionID session.ID, sender VerifiedSender, event *events.Event) (*events.Event, error) {
	response, err := b.ingress.HandleVerifiedInteraction(ctx, sessionID, interaction.VerifiedExternalSender{
		Platform: sender.Platform, SubjectID: sender.SubjectID, Claims: sender.Claims,
	}, event)
	if err != nil {
		return nil, err
	}
	if err := b.replay(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (b *boundIngress) HandleSessionEvent(ctx context.Context, sender VerifiedSender, event sessionevent.Event) (*permissionrequest.Request, error) {
	if b.version < StructuredABIVersion {
		return nil, ErrUnsupportedABI
	}
	request, err := b.ingress.HandleSessionEvent(ctx, interaction.VerifiedExternalSender{
		Platform: sender.Platform, SubjectID: sender.SubjectID, Claims: sender.Claims,
	}, event)
	if err != nil {
		return nil, err
	}
	if err := b.replay(ctx); err != nil {
		return nil, err
	}
	return request, nil
}
