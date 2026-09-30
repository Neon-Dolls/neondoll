package relay

import (
	"encoding/json"
	"fmt"
)

// controlHeader is used to determine the message type during unmarshal.
type controlHeader struct {
	Type ControlMessageType `json:"type"`
}

// MarshalControl marshals any control message to JSON.
func MarshalControl(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UnmarshalControl unmarshals JSON into the correct control message struct
// based on the "type" discriminator field.
func UnmarshalControl(data []byte) (any, error) {
	var h controlHeader
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("relay: unmarshal control header: %w", err)
	}

	var msg any
	switch h.Type {
	case CmdRegister:
		msg = &Register{}
	case CmdRegistered:
		msg = &Registered{}
	case CmdRouteOpen:
		msg = &RouteOpen{}
	case CmdRouteOpened:
		msg = &RouteOpened{}
	case CmdRouteClose:
		msg = &RouteClose{}
	case CmdRouteClosed:
		msg = &RouteClosed{}
	case CmdError:
		msg = &RelayError{}
	default:
		return nil, fmt.Errorf("relay: unknown control message type: %q", h.Type)
	}

	if err := json.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("relay: unmarshal control message: %w", err)
	}
	return msg, nil
}

// ValidateControl checks that required fields are present in a control message.
func ValidateControl(v any) error {
	switch m := v.(type) {
	case *Register:
		if m.Token == "" {
			return fmt.Errorf("relay: Register.token is required")
		}
	case *Registered:
		if m.RelayID == "" {
			return fmt.Errorf("relay: Registered.relay_id is required")
		}
	case *RouteOpen:
		if m.RouteID == 0 {
			return fmt.Errorf("relay: RouteOpen.route_id is required")
		}
		if m.SourceAddr == "" {
			return fmt.Errorf("relay: RouteOpen.source_addr is required")
		}
		if m.TargetAddr == "" {
			return fmt.Errorf("relay: RouteOpen.target_addr is required")
		}
	case *RouteOpened:
		if m.RouteID == 0 {
			return fmt.Errorf("relay: RouteOpened.route_id is required")
		}
		if m.SourceAddr == "" {
			return fmt.Errorf("relay: RouteOpened.source_addr is required")
		}
	case *RouteClose:
		if m.RouteID == 0 {
			return fmt.Errorf("relay: RouteClose.route_id is required")
		}
	case *RouteClosed:
		if m.RouteID == 0 {
			return fmt.Errorf("relay: RouteClosed.route_id is required")
		}
	case *RelayError:
		if m.Code == "" {
			return fmt.Errorf("relay: RelayError.code is required")
		}
	default:
		return fmt.Errorf("relay: unknown control message type %T", v)
	}
	return nil
}
