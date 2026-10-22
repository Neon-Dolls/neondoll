package sessioncontrol

import (
	"errors"
	"strings"
)

var (
	ErrInvalidRequest     = errors.New("session control: invalid request")
	ErrUnsupportedVersion = errors.New("session control: unsupported version")
)

func (request Request) Validate() error {
	if request.Version != Version {
		return ErrUnsupportedVersion
	}
	if strings.TrimSpace(request.ID) == "" {
		return ErrInvalidRequest
	}
	switch request.Type {
	case TypeCreate:
		if request.Payload.SessionID != "" {
			return ErrInvalidRequest
		}
	case TypeList:
		if request.Payload.SessionID != "" || request.Payload.ExternalConversationRef != "" {
			return ErrInvalidRequest
		}
	case TypeSelect, TypeClose:
		if strings.TrimSpace(request.Payload.SessionID) == "" || request.Payload.ExternalConversationRef != "" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func (interaction Interaction) Validate() error {
	if interaction.Version != Version {
		return ErrUnsupportedVersion
	}
	if interaction.Type != TypeInteraction || strings.TrimSpace(interaction.SessionID) == "" || strings.TrimSpace(interaction.Event.ID) == "" {
		return ErrInvalidRequest
	}
	return nil
}
