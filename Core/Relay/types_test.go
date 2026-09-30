package relay

import (
	"testing"
)

func TestControlMessageTypeConstants(t *testing.T) {
	tests := []struct {
		name  string
		value ControlMessageType
		want  string
	}{
		{"CmdRegister", CmdRegister, "register"},
		{"CmdRegistered", CmdRegistered, "registered"},
		{"CmdRouteOpen", CmdRouteOpen, "route_open"},
		{"CmdRouteOpened", CmdRouteOpened, "route_opened"},
		{"CmdRouteClose", CmdRouteClose, "route_close"},
		{"CmdRouteClosed", CmdRouteClosed, "route_closed"},
		{"CmdError", CmdError, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.value) != tt.want {
				t.Errorf("got %q, want %q", string(tt.value), tt.want)
			}
		})
	}
}

func TestErrorCodeConstants(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"ErrRateLimited", ErrRateLimited, "rate_limited"},
		{"ErrAuthFailed", ErrAuthFailed, "auth_failed"},
		{"ErrRouteLimit", ErrRouteLimit, "route_limit"},
		{"ErrNotFound", ErrNotFound, "not_found"},
		{"ErrInternal", ErrInternal, "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != tt.want {
				t.Errorf("got %q, want %q", tt.value, tt.want)
			}
		})
	}
}

func TestFrameTypeConstants(t *testing.T) {
	if FrameTypeUnspecified != 0 {
		t.Errorf("FrameTypeUnspecified: got %d, want 0", FrameTypeUnspecified)
	}
	if FrameTypeWireGuard != 1 {
		t.Errorf("FrameTypeWireGuard: got %d, want 1", FrameTypeWireGuard)
	}
}

func TestProtocolConstants(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion: got %d, want 1", ProtocolVersion)
	}
	if FrameHeaderSize != 11 {
		t.Errorf("FrameHeaderSize: got %d, want 11", FrameHeaderSize)
	}
	if MaxFramePayloadSize != 65535 {
		t.Errorf("MaxFramePayloadSize: got %d, want 65535", MaxFramePayloadSize)
	}
	if MaxRouteID != 1<<63-1 {
		t.Errorf("MaxRouteID: got %d, want %d", MaxRouteID, uint64(1<<63-1))
	}
}

func TestErrorVars(t *testing.T) {
	if ErrNotConnected == nil {
		t.Error("ErrNotConnected is nil")
	}
	if ErrAlreadyClosed == nil {
		t.Error("ErrAlreadyClosed is nil")
	}
	if ErrRouteInUse == nil {
		t.Error("ErrRouteInUse is nil")
	}
	if ErrRouteNotFound == nil {
		t.Error("ErrRouteNotFound is nil")
	}
}
