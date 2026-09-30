package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalAndUnmarshalControl_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  any
	}{
		{
			name: "Register",
			msg:  &Register{Type: CmdRegister, Token: "relay-token"},
		},
		{
			name: "Registered",
			msg:  &Registered{Type: CmdRegistered, RelayID: "relay-01"},
		},
		{
			name: "RouteOpen",
			msg:  &RouteOpen{Type: CmdRouteOpen, RouteID: 1, Credentials: RouteCredentials{Token: "route-token"}},
		},
		{
			name: "RouteOpened",
			msg:  &RouteOpened{Type: CmdRouteOpened, RouteID: 1, AllocatedEndpoint: "relay.example.net:42023"},
		},
		{
			name: "RouteClose",
			msg:  &RouteClose{Type: CmdRouteClose, RouteID: 1},
		},
		{
			name: "RouteClosed",
			msg:  &RouteClosed{Type: CmdRouteClosed, RouteID: 1},
		},
		{
			name: "RelayError",
			msg:  &RelayError{Type: CmdError, Code: ErrNotFound, Message: "route not found"},
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

			// Re-marshal the result to compare JSON equality
			wantJSON, _ := json.Marshal(tt.msg)
			gotJSON, _ := json.Marshal(got)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("JSON mismatch:\n  want: %s\n  got:  %s", string(wantJSON), string(gotJSON))
			}
		})
	}
}

func TestUnmarshalControl_Errors(t *testing.T) {
	t.Parallel()

	t.Run("truncated", func(t *testing.T) {
		_, err := UnmarshalControl([]byte(`{"type": "register"`))
		if err == nil {
			t.Fatal("expected error for truncated JSON")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		_, err := UnmarshalControl([]byte(`{"type": "unknown_stuff"}`))
		if err == nil {
			t.Fatal("expected error for unknown type")
		}
	})

	t.Run("empty data", func(t *testing.T) {
		_, err := UnmarshalControl([]byte{})
		if err == nil {
			t.Fatal("expected error for empty data")
		}
	})

	t.Run("oversized message", func(t *testing.T) {
		// Create a JSON payload larger than MaxControlMessageSize
		data := make([]byte, MaxControlMessageSize+1)
		data[0] = '{'
		data[len(data)-1] = '}'
		_, err := UnmarshalControl(data)
		if err == nil {
			t.Fatal("expected error for oversized control message")
		}
		if !strings.Contains(err.Error(), "exceeds max") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestValidateControl(t *testing.T) {
	t.Parallel()

	t.Run("Register — valid", func(t *testing.T) {
		err := ValidateControl(&Register{Type: CmdRegister, Token: "tok"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("Register — missing token", func(t *testing.T) {
		err := ValidateControl(&Register{Type: CmdRegister})
		if err == nil {
			t.Fatal("expected error for missing token")
		}
	})

	t.Run("Registered — valid", func(t *testing.T) {
		err := ValidateControl(&Registered{Type: CmdRegistered, RelayID: "rid"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("Registered — missing relay_id", func(t *testing.T) {
		err := ValidateControl(&Registered{Type: CmdRegistered})
		if err == nil {
			t.Fatal("expected error for missing relay_id")
		}
	})

	t.Run("RouteOpen — valid", func(t *testing.T) {
		err := ValidateControl(&RouteOpen{
			Type:        CmdRouteOpen,
			RouteID:     1,
			Credentials: RouteCredentials{Token: "route-token"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("RouteOpen — missing route_id", func(t *testing.T) {
		err := ValidateControl(&RouteOpen{
			Type:        CmdRouteOpen,
			Credentials: RouteCredentials{Token: "route-token"},
		})
		if err == nil {
			t.Fatal("expected error for missing route_id")
		}
	})

	t.Run("RouteOpen — missing credentials token", func(t *testing.T) {
		err := ValidateControl(&RouteOpen{
			Type:    CmdRouteOpen,
			RouteID: 1,
		})
		if err == nil {
			t.Fatal("expected error for missing credentials token")
		}
	})

	t.Run("RouteOpened — valid", func(t *testing.T) {
		err := ValidateControl(&RouteOpened{
			Type:              CmdRouteOpened,
			RouteID:           1,
			AllocatedEndpoint: "relay.example.net:42023",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("RouteOpened — missing route_id", func(t *testing.T) {
		err := ValidateControl(&RouteOpened{
			Type:              CmdRouteOpened,
			AllocatedEndpoint: "relay.example.net:42023",
		})
		if err == nil {
			t.Fatal("expected error for missing route_id")
		}
	})

	t.Run("RouteOpened — missing endpoint", func(t *testing.T) {
		err := ValidateControl(&RouteOpened{
			Type:    CmdRouteOpened,
			RouteID: 1,
		})
		if err == nil {
			t.Fatal("expected error for missing endpoint")
		}
	})

	t.Run("RouteClose — valid", func(t *testing.T) {
		err := ValidateControl(&RouteClose{Type: CmdRouteClose, RouteID: 1})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("RouteClose — missing route_id", func(t *testing.T) {
		err := ValidateControl(&RouteClose{Type: CmdRouteClose})
		if err == nil {
			t.Fatal("expected error for missing route_id")
		}
	})

	t.Run("RouteClosed — valid", func(t *testing.T) {
		err := ValidateControl(&RouteClosed{Type: CmdRouteClosed, RouteID: 1})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("RouteClosed — missing route_id", func(t *testing.T) {
		err := ValidateControl(&RouteClosed{Type: CmdRouteClosed})
		if err == nil {
			t.Fatal("expected error for missing route_id")
		}
	})

	t.Run("RelayError — valid", func(t *testing.T) {
		err := ValidateControl(&RelayError{Type: CmdError, Code: ErrNotFound})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("RelayError — missing code", func(t *testing.T) {
		err := ValidateControl(&RelayError{Type: CmdError})
		if err == nil {
			t.Fatal("expected error for missing code")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		err := ValidateControl("not a pointer")
		if err == nil {
			t.Fatal("expected error for unknown type")
		}
	})
}

func TestControlFieldLabels(t *testing.T) {
	t.Parallel()
	// Verify JSON serialization produces the correct field names.

	t.Run("RouteOpen — no proxy semantics", func(t *testing.T) {
		m := RouteOpen{
			Type:        CmdRouteOpen,
			RouteID:     7,
			Credentials: RouteCredentials{Token: "rtok"},
		}
		data, _ := json.Marshal(m)
		// Must NOT include source_addr or target_addr
		if containsJSONKey(string(data), "source_addr") {
			t.Error("RouteOpen should not serialize source_addr")
		}
		if containsJSONKey(string(data), "target_addr") {
			t.Error("RouteOpen should not serialize target_addr")
		}
		// Must include credentials
		if !containsJSONKey(string(data), "credentials") {
			t.Error("RouteOpen should serialize credentials")
		}
	})

	t.Run("RouteOpened — endpoint field", func(t *testing.T) {
		m := RouteOpened{
			Type:              CmdRouteOpened,
			RouteID:           7,
			AllocatedEndpoint: "relay.example.net:42023",
		}
		data, _ := json.Marshal(m)
		if !containsJSONKey(string(data), "endpoint") {
			t.Error("RouteOpened should serialize endpoint, got:", string(data))
		}
	})
}

func containsJSONKey(jsonStr, key string) bool {
	// Quick check: key followed by ':' in JSON context
	idx := strings.Index(jsonStr, "\""+key+"\"")
	if idx == -1 {
		return false
	}
	// Ensure there's a ':' after the key name
	remaining := jsonStr[idx+len(key)+2:]
	return len(remaining) > 0 && remaining[0] == ':'
}
