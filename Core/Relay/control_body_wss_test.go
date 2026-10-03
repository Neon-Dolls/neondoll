package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ── Body WSS Attachment (M5.1) tests ─────────────────────────────────
//
// These tests exercise the Body-facing WebSocket attachment endpoint at
// /body on the ControlServer.
//
// Each test sets up a Service + ControlServer, then uses the Registry
// directly to create registrations and open routes with known credentials
// (avoiding the full E2E ControlClient path which would require keeping
// a Core WebSocket alive throughout the test).

// makeBodyWS connects a raw WebSocket to /body, sends BodyAttach, and
// returns the connection plus the parsed response.
func makeBodyWS(addr string, routeID RouteID, credential string) (*websocket.Conn, any, error) {
	dialer := &websocket.Dialer{}
	ws, _, err := dialer.Dial(fmt.Sprintf("ws://%s/body", addr), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial /body: %w", err)
	}

	// Send attach message as text frame
	body, marshalErr := json.Marshal(&BodyAttach{
		Version:    ProtocolVersion,
		Type:       CmdBodyAttach,
		RouteID:    routeID,
		Credential: credential,
	})
	if marshalErr != nil {
		ws.Close()
		return nil, nil, fmt.Errorf("marshal attach: %w", marshalErr)
	}
	if err := ws.WriteMessage(websocket.TextMessage, body); err != nil {
		ws.Close()
		return nil, nil, fmt.Errorf("write attach: %w", err)
	}

	// Read response
	msgType, raw, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if msgType != websocket.TextMessage {
		ws.Close()
		return nil, nil, fmt.Errorf("expected text message, got %d", msgType)
	}

	resp, err := UnmarshalControl(raw)
	if err != nil {
		ws.Close()
		return nil, nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return ws, resp, nil
}

// bodyAttachOrError is like makeBodyWS but returns only the response.
// The WS is always closed before returning.
func bodyAttachOrError(addr string, routeID RouteID, credential string) (any, error) {
	ws, resp, err := makeBodyWS(addr, routeID, credential)
	if ws != nil {
		ws.Close()
	}
	return resp, err
}

// openTestRoute uses the Registry directly to set up a route with
// known credentials. The registration is created automatically.
// Repeated calls with the same regID are safe; subsequent calls skip
// AddRegistration.
func openTestRoute(svc *Service, regID RegistrationID, routeID RouteID, token string) error {
	regErr := svc.Registry().AddRegistration(regID)
	if regErr != nil && regErr != ErrRegistrationExists {
		return fmt.Errorf("AddRegistration: %w", regErr)
	}
	if _, err := svc.Registry().AllocateRoute(regID, routeID); err != nil {
		return fmt.Errorf("AllocateRoute: %w", err)
	}
	if err := svc.Registry().SetRouteCredentials(regID, routeID, RouteCredentials{Token: token}); err != nil {
		return fmt.Errorf("SetRouteCredentials: %w", err)
	}
	if _, err := svc.Registry().OpenRoute(regID, routeID); err != nil {
		return fmt.Errorf("OpenRoute: %w", err)
	}
	return nil
}

func makeServiceAndServer(t *testing.T) (*Service, *ControlServer, string) {
	t.Helper()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
		return nil, nil, ""
	}

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(context.Background())
	defer func() { csCancel() }()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}
	return svc, cs, addr
}

// ── Tests ─────────────────────────────────────────────────────────────

// TestBodyAttach_ValidCredential proves that a valid route credential
// successfully attaches, returning BodyAttached with the correct route ID.
func TestBodyAttach_ValidCredential(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "test-credential"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	beforeBody := len(cs.body)

	bodyWS, resp, err := makeBodyWS(addr, RouteID(1), "test-credential")
	if err != nil {
		t.Fatalf("makeBodyWS: %v", err)
	}
	defer bodyWS.Close()

	// Verify BodyAttached response
	attached, ok := resp.(*BodyAttached)
	if !ok {
		t.Fatalf("response is not BodyAttached; got %T", resp)
	}
	if attached.Version != ProtocolVersion {
		t.Errorf("BodyAttached.version = %d; want %d", attached.Version, ProtocolVersion)
	}
	if attached.RouteID != RouteID(1) {
		t.Errorf("BodyAttached.route_id = %d; want 1", attached.RouteID)
	}

	afterBody := len(cs.body)
	if afterBody != beforeBody+1 {
		t.Errorf("body conns before=%d after=%d; want %d (+1)", beforeBody, afterBody, beforeBody+1)
	}
}

// TestBodyAttach_WrongCredential proves that attaching with an incorrect
// credential is rejected and creates no attachment.
func TestBodyAttach_WrongCredential(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "correct-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	beforeBody := len(cs.body)

	resp, err := bodyAttachOrError(addr, RouteID(1), "wrong-cred")
	if err != nil {
		t.Fatalf("bodyAttachOrError: %v", err)
	}

	re, ok := resp.(*RelayError)
	if !ok {
		t.Fatalf("response is not RelayError; got %T", resp)
	}
	if re.Code != ErrAuthFailed {
		t.Errorf("error code = %q; want %q", re.Code, ErrAuthFailed)
	}

	afterBody := len(cs.body)
	if afterBody != beforeBody {
		t.Errorf("body conns before=%d after=%d; want %d (unchanged)", beforeBody, afterBody, beforeBody)
	}
}

// TestBodyAttach_EmptyCredential proves that an empty credential is rejected.
func TestBodyAttach_EmptyCredential(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "some-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	beforeBody := len(cs.body)

	resp, err := bodyAttachOrError(addr, RouteID(1), "")
	if err != nil {
		t.Fatalf("bodyAttachOrError: %v", err)
	}

	re, ok := resp.(*RelayError)
	if !ok {
		t.Fatalf("response is not RelayError; got %T", resp)
	}
	if re.Code != ErrAuthFailed {
		t.Errorf("error code = %q; want %q", re.Code, ErrAuthFailed)
	}

	afterBody := len(cs.body)
	if afterBody != beforeBody {
		t.Errorf("body conns before=%d after=%d; want %d (unchanged)", beforeBody, afterBody, beforeBody)
	}
}

// TestBodyAttach_UnknownRoute proves that attaching to a non-existent
// route returns the same error as wrong credential (no enumeration).
func TestBodyAttach_UnknownRoute(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	beforeBody := len(cs.body)

	// Unknown route 999
	resp, err := bodyAttachOrError(addr, RouteID(999), "some-credential")
	if err != nil {
		t.Fatalf("bodyAttachOrError: %v", err)
	}

	re, ok := resp.(*RelayError)
	if !ok {
		t.Fatalf("response is not RelayError; got %T", resp)
	}
	if re.Code != ErrAuthFailed {
		t.Errorf("error code = %q; want %q", re.Code, ErrAuthFailed)
	}

	afterBody := len(cs.body)
	if afterBody != beforeBody {
		t.Errorf("body conns before=%d after=%d; want %d (unchanged)", beforeBody, afterBody, beforeBody)
	}
}

// TestBodyAttach_RouteA_Cred_Cannot_Attach_RouteB proves that a
// credential valid for Route A cannot attach to Route B.
func TestBodyAttach_RouteA_Cred_Cannot_Attach_RouteB(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "cred-A"); err != nil {
		t.Fatalf("openTestRoute route 1: %v", err)
	}
	if err := openTestRoute(svc, regID, RouteID(2), "cred-B"); err != nil {
		t.Fatalf("openTestRoute route 2: %v", err)
	}

	beforeBody := len(cs.body)

	// Route 1's credential ("cred-A") cannot attach to Route 2
	resp, err := bodyAttachOrError(addr, RouteID(2), "cred-A")
	if err != nil {
		t.Fatalf("bodyAttachOrError: %v", err)
	}

	re, ok := resp.(*RelayError)
	if !ok {
		t.Fatalf("response is not RelayError; got %T", resp)
	}
	if re.Code != ErrAuthFailed {
		t.Errorf("error code = %q; want %q", re.Code, ErrAuthFailed)
	}

	afterBody := len(cs.body)
	if afterBody != beforeBody {
		t.Errorf("body conns before=%d after=%d; want %d (unchanged)", beforeBody, afterBody, beforeBody)
	}
}

// TestBodyAttach_RouteA_Cannot_Affect_RouteB proves that a Body attached
// to Route A does not create any attachment on Route B.
func TestBodyAttach_RouteA_Cannot_Affect_RouteB(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "cred-A"); err != nil {
		t.Fatalf("openTestRoute route 1: %v", err)
	}
	if err := openTestRoute(svc, regID, RouteID(2), "cred-B"); err != nil {
		t.Fatalf("openTestRoute route 2: %v", err)
	}

	// Authenticate to Route 1
	bodyWS, resp1, err := makeBodyWS(addr, RouteID(1), "cred-A")
	if err != nil {
		t.Fatalf("makeBodyWS Route 1: %v", err)
	}
	attached, ok := resp1.(*BodyAttached)
	if !ok {
		t.Fatalf("Route 1 attach response is not BodyAttached; got %T", resp1)
	}
	if attached.RouteID != RouteID(1) {
		t.Errorf("Route 1 attached RouteID = %d; want 1", attached.RouteID)
	}

	// Route 2 should have no body attachment
	cs.mu.RLock()
	bwc2 := cs.body[RouteID(2)]
	cs.mu.RUnlock()
	if bwc2 != nil {
		t.Error("Route 2 has body attachment before it was attached")
	}

	_ = bodyWS
}

// TestBodyAttach_FailedAuth_LeavesNoAttachment proves that after a failed
// authentication, no residual body WS handle remains on the route.
func TestBodyAttach_FailedAuth_LeavesNoAttachment(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "some-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// Try to attach with wrong credential
	_, bodyErr := bodyAttachOrError(addr, RouteID(1), "wrong")
	if bodyErr != nil {
		t.Fatalf("bodyAttachOrError: %v", bodyErr)
	}

	// Route's body slot must be empty
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc != nil {
		t.Error("route 1 has body attachment after failed auth")
	}
}

// TestBodyAttach_DisconnectRemovesAttachment proves that disconnect
// cleans up the route's body attachment without closing the route.
func TestBodyAttach_DisconnectRemovesAttachment(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "test-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// Attach Body
	bodyWS, resp, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("makeBodyWS: %v", err)
	}
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach response not BodyAttached; got %T", resp)
	}

	// Verify attachment exists
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("route 1 has no body attachment after successful attach")
	}

	// Disconnect
	bodyWS.Close()
	time.Sleep(300 * time.Millisecond)

	// Attachment removed
	cs.mu.RLock()
	bwc = cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc != nil {
		t.Error("route 1 has stale body attachment after disconnect")
	}

	// Route still open (disconnect does NOT close it)
	entry, ok := svc.Registry().Route(RouteID(1))
	if !ok {
		t.Fatal("route 1 vanished after body disconnect")
	}
	if entry.State != RouteStateOpen {
		t.Errorf("route 1 state after body disconnect: want RouteStateOpen, got %s", entry.State)
	}
}

// TestBodyAttach_Replacement proves that a second Body attachment replaces
// the first cleanly.
func TestBodyAttach_Replacement(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "test-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// First Body attaches
	bodyWS1, resp1, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("first makeBodyWS: %v", err)
	}
	if _, ok := resp1.(*BodyAttached); !ok {
		t.Fatalf("first attach response not BodyAttached")
	}

	// Second Body attaches with same credential — replaces first
	bodyWS2, resp2, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("second makeBodyWS: %v", err)
	}
	if _, ok := resp2.(*BodyAttached); !ok {
		t.Fatalf("second attach response not BodyAttached")
	}

	time.Sleep(300 * time.Millisecond)

	// New attachment exists
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("route 1 has no body attachment after replacement")
	}

	// Old body WS should be closed (read returns error)
	_, _, readErr := bodyWS1.ReadMessage()
	if readErr == nil {
		t.Error("old body WS not closed after replacement")
	}

	_ = bodyWS2
}

// TestBodyAttach_ReconnectRequiresAuth proves that reconnecting after
// disconnect requires a fresh authentication.
func TestBodyAttach_ReconnectRequiresAuth(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "test-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// First connection — authenticate with correct credential
	bodyWS1, resp1, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("first makeBodyWS: %v", err)
	}
	if _, ok := resp1.(*BodyAttached); !ok {
		t.Fatalf("first attach: expected BodyAttached, got %T", resp1)
	}
	bodyWS1.Close()
	time.Sleep(300 * time.Millisecond)

	// Second connection — must authenticate again, succeeds with correct
	// credential.
	resp2, err := bodyAttachOrError(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("second bodyAttachOrError: %v", err)
	}
	if _, ok := resp2.(*BodyAttached); !ok {
		t.Fatalf("second attach: expected BodyAttached, got %T", resp2)
	}

	// Without credential — must fail
	resp3, err := bodyAttachOrError(addr, RouteID(1), "")
	if err != nil {
		t.Fatalf("third bodyAttachOrError: %v", err)
	}
	if _, ok := resp3.(*RelayError); !ok {
		t.Fatalf("third attach (empty): expected RelayError, got %T", resp3)
	}
}

// TestBodyAttach_RouteRegistrationIsolation proves that body attachment
// lifecycle does not mutate registration or route identity.
func TestBodyAttach_RouteRegistrationIsolation(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { svc.Start(ctx) }()
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "test-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// Capture identity state before body attachment
	beforeRegCount := svc.Registry().RegistrationCount()
	beforeRouteReg, _ := svc.Registry().RouteRegistration(RouteID(1))
	beforeRouteEntry, _ := svc.Registry().Route(RouteID(1))

	// Attach Body
	bodyWS, resp, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("makeBodyWS: %v", err)
	}
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach: expected BodyAttached, got %T", resp)
	}

	// No identity mutation from attachment
	afterRegCount := svc.Registry().RegistrationCount()
	if afterRegCount != beforeRegCount {
		t.Errorf("registration count changed: before=%d after=%d", beforeRegCount, afterRegCount)
	}
	afterRouteReg, _ := svc.Registry().RouteRegistration(RouteID(1))
	if afterRouteReg != beforeRouteReg {
		t.Errorf("route registration changed: before=%q after=%q", beforeRouteReg, afterRouteReg)
	}
	afterRouteEntry, _ := svc.Registry().Route(RouteID(1))
	if afterRouteEntry.Credentials.Token != beforeRouteEntry.Credentials.Token {
		t.Errorf("route credentials changed after body attach")
	}
	if afterRouteEntry.State != beforeRouteEntry.State {
		t.Errorf("route state changed after body attach: before=%s after=%s", beforeRouteEntry.State, afterRouteEntry.State)
	}

	// Disconnect
	bodyWS.Close()
	time.Sleep(300 * time.Millisecond)

	afterDisconnectRegCount := svc.Registry().RegistrationCount()
	if afterDisconnectRegCount != beforeRegCount {
		t.Errorf("registration count changed after body disconnect: before=%d after=%d", beforeRegCount, afterDisconnectRegCount)
	}
	afterDisconnectRouteReg, _ := svc.Registry().RouteRegistration(RouteID(1))
	if afterDisconnectRouteReg != beforeRouteReg {
		t.Errorf("route registration changed after body disconnect: before=%q after=%q", beforeRouteReg, afterDisconnectRouteReg)
	}
}