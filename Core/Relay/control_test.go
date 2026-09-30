package relay

import (
	"encoding/json"
	"testing"
)

func TestMarshalControl_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want ControlMessageType
	}{
		{
			name: "register",
			msg: &Register{
				Type:  CmdRegister,
				Token: "secret-token",
			},
			want: CmdRegister,
		},
		{
			name: "registered",
			msg: &Registered{
				Type:    CmdRegistered,
				RelayID: "relay-01",
			},
			want: CmdRegistered,
		},
		{
			name: "route_open",
			msg: &RouteOpen{
				Type:       CmdRouteOpen,
				RouteID:    42,
				SourceAddr: "10.0.0.1",
				TargetAddr: "10.0.0.2",
			},
			want: CmdRouteOpen,
		},
		{
			name: "route_opened",
			msg: &RouteOpened{
				Type:       CmdRouteOpened,
				RouteID:    42,
				SourceAddr: "10.0.0.1",
			},
			want: CmdRouteOpened,
		},
		{
			name: "route_close",
			msg: &RouteClose{
				Type:    CmdRouteClose,
				RouteID: 42,
			},
			want: CmdRouteClose,
		},
		{
			name: "route_closed",
			msg: &RouteClosed{
				Type:    CmdRouteClosed,
				RouteID: 42,
			},
			want: CmdRouteClosed,
		},
		{
			name: "error",
			msg: &RelayError{
				Type:    CmdError,
				Code:    "rate_limited",
				Message: "too many requests",
			},
			want: CmdError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := MarshalControl(tt.msg)
			if err != nil {
				t.Fatalf("MarshalControl: %v", err)
			}

			got, err := UnmarshalControl(data)
			if err != nil {
				t.Fatalf("UnmarshalControl: %v", err)
			}

			// Verify the type field matches
			var header struct {
				Type ControlMessageType `json:"type"`
			}
			if err := json.Unmarshal(data, &header); err != nil {
				t.Fatalf("json.Unmarshal header: %v", err)
			}
			if header.Type != tt.want {
				t.Errorf("type: got %q, want %q", header.Type, tt.want)
			}

			// Verify round-trip preserves fields by re-marshaling
			reData, err := MarshalControl(got)
			if err != nil {
				t.Fatalf("re-MarshalControl: %v", err)
			}

			var orig, round any
			if err := json.Unmarshal(data, &orig); err != nil {
				t.Fatalf("json.Unmarshal orig: %v", err)
			}
			if err := json.Unmarshal(reData, &round); err != nil {
				t.Fatalf("json.Unmarshal round: %v", err)
			}

			origJSON, _ := json.Marshal(orig)
			roundJSON, _ := json.Marshal(round)
			if string(origJSON) != string(roundJSON) {
				t.Errorf("round-trip mismatch:\n  orig:  %s\n  round: %s", origJSON, roundJSON)
			}
		})
	}
}

func TestUnmarshalControl_UnknownType(t *testing.T) {
	data := `{"type": "nonexistent"}`
	_, err := UnmarshalControl([]byte(data))
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
}

func TestUnmarshalControl_InvalidJSON(t *testing.T) {
	_, err := UnmarshalControl([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestValidateControl(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		ok   bool
	}{
		{
			name: "valid register",
			msg:  &Register{Type: CmdRegister, Token: "abc"},
			ok:   true,
		},
		{
			name: "register missing token",
			msg:  &Register{Type: CmdRegister},
			ok:   false,
		},
		{
			name: "valid registered",
			msg:  &Registered{Type: CmdRegistered, RelayID: "r1"},
			ok:   true,
		},
		{
			name: "registered missing relay_id",
			msg:  &Registered{Type: CmdRegistered},
			ok:   false,
		},
		{
			name: "valid route_open",
			msg:  &RouteOpen{Type: CmdRouteOpen, RouteID: 1, SourceAddr: "a", TargetAddr: "b"},
			ok:   true,
		},
		{
			name: "route_open missing route_id",
			msg:  &RouteOpen{Type: CmdRouteOpen, SourceAddr: "a", TargetAddr: "b"},
			ok:   false,
		},
		{
			name: "route_open missing source_addr",
			msg:  &RouteOpen{Type: CmdRouteOpen, RouteID: 1, TargetAddr: "b"},
			ok:   false,
		},
		{
			name: "route_open missing target_addr",
			msg:  &RouteOpen{Type: CmdRouteOpen, RouteID: 1, SourceAddr: "a"},
			ok:   false,
		},
		{
			name: "valid route_opened",
			msg:  &RouteOpened{Type: CmdRouteOpened, RouteID: 1, SourceAddr: "a"},
			ok:   true,
		},
		{
			name: "route_opened missing source_addr",
			msg:  &RouteOpened{Type: CmdRouteOpened, RouteID: 1},
			ok:   false,
		},
		{
			name: "valid route_close",
			msg:  &RouteClose{Type: CmdRouteClose, RouteID: 1},
			ok:   true,
		},
		{
			name: "route_close missing route_id",
			msg:  &RouteClose{Type: CmdRouteClose},
			ok:   false,
		},
		{
			name: "valid route_closed",
			msg:  &RouteClosed{Type: CmdRouteClosed, RouteID: 1},
			ok:   true,
		},
		{
			name: "route_closed missing route_id",
			msg:  &RouteClosed{Type: CmdRouteClosed},
			ok:   false,
		},
		{
			name: "valid error",
			msg:  &RelayError{Type: CmdError, Code: "err"},
			ok:   true,
		},
		{
			name: "error missing code",
			msg:  &RelayError{Type: CmdError},
			ok:   false,
		},
		{
			name: "unknown type",
			msg:  "not a control message",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateControl(tt.msg)
			if tt.ok && err != nil {
				t.Fatalf("ValidateControl: unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("ValidateControl: expected error, got nil")
			}
		})
	}
}
