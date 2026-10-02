package relay

import (
	"testing"
)

func TestControlMessageTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		got  ControlMessageType
		want ControlMessageType
	}{
		{CmdRegister, "register"},
		{CmdRegistered, "registered"},
		{CmdRouteOpen, "route_open"},
		{CmdRouteOpened, "route_opened"},
		{CmdRouteClose, "route_close"},
		{CmdRouteClosed, "route_closed"},
		{CmdError, "error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.got), func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestErrorCodeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		got  string
		want string
	}{
		{ErrRateLimited, "rate_limited"},
		{ErrAuthFailed, "auth_failed"},
		{ErrRouteLimit, "route_limit"},
		{ErrNotFound, "not_found"},
		{ErrInternal, "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestNetworkConstants(t *testing.T) {
	t.Parallel()

	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", ProtocolVersion)
	}
	if FrameHeaderSize != 11 {
		t.Errorf("FrameHeaderSize = %d, want 11", FrameHeaderSize)
	}
	if MaxFramePayloadSize != 65535 {
		t.Errorf("MaxFramePayloadSize = %d, want 65535", MaxFramePayloadSize)
	}
	if MaxRouteID != 1<<63-1 {
		t.Errorf("MaxRouteID = %d", MaxRouteID)
	}
	if MaxControlMessageSize != 65536 {
		t.Errorf("MaxControlMessageSize = %d, want 65536", MaxControlMessageSize)
	}
}
