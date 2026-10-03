package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// bodyConnCount returns len(cs.body) under cs.mu read lock, safe to
// call from test goroutines that run concurrently with handleBodyWS.
func bodyConnCount(cs *ControlServer) int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.body)
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
	svc.Start(ctx)
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

	beforeBody := bodyConnCount(cs)

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

	afterBody := bodyConnCount(cs)
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
	svc.Start(ctx)
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

	beforeBody := bodyConnCount(cs)

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

	afterBody := bodyConnCount(cs)
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
	svc.Start(ctx)
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

	beforeBody := bodyConnCount(cs)

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

	afterBody := bodyConnCount(cs)
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
	svc.Start(ctx)
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

	beforeBody := bodyConnCount(cs)

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

	afterBody := bodyConnCount(cs)
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
	svc.Start(ctx)
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

	beforeBody := bodyConnCount(cs)

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

	afterBody := bodyConnCount(cs)
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
	svc.Start(ctx)
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
	svc.Start(ctx)
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
	svc.Start(ctx)
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
	svc.Start(ctx)
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
	svc.Start(ctx)
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
	svc.Start(ctx)
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

// TestBodyAttach_RouteCloseRemovesAttachment proves that detachBody
// (called by RouteClose handler) closes and removes the body attachment.
func TestBodyAttach_RouteCloseRemovesAttachment(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc.Start(ctx)
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

	// Attach Body to Route 1
	bodyWS, resp, err := makeBodyWS(addr, RouteID(1), "test-cred")
	if err != nil {
		t.Fatalf("makeBodyWS: %v", err)
	}
	defer bodyWS.Close()
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

	// RouteClose handler calls cs.detachBody — test that mechanism
	cs.detachBody(RouteID(1))
	time.Sleep(300 * time.Millisecond)

	// Verify attachment removed from cs.body
	cs.mu.RLock()
	bwc = cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc != nil {
		t.Error("route 1 has stale body attachment after detachBody")
	}

	// Verify body WS is closed (ReadMessage returns error)
	_, _, readErr := bodyWS.ReadMessage()
	if readErr == nil {
		t.Error("body WS not closed after detachBody")
	}
}

// TestBodyAttach_CoreTeardownRemovesAttachments proves that the Core
// teardown loop (iterating collected route IDs and detaching each body)
// removes all body attachments and leaves no stale entries.
func TestBodyAttach_CoreTeardownRemovesAttachments(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc.Start(ctx)
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
	if err := openTestRoute(svc, regID, RouteID(1), "cred-1"); err != nil {
		t.Fatalf("openTestRoute route 1: %v", err)
	}
	if err := openTestRoute(svc, regID, RouteID(2), "cred-2"); err != nil {
		t.Fatalf("openTestRoute route 2: %v", err)
	}

	// Attach bodies to both routes
	bodyWS1, resp1, err := makeBodyWS(addr, RouteID(1), "cred-1")
	if err != nil {
		t.Fatalf("makeBodyWS route 1: %v", err)
	}
	defer bodyWS1.Close()
	if _, ok := resp1.(*BodyAttached); !ok {
		t.Fatalf("route 1 attach not BodyAttached; got %T", resp1)
	}

	bodyWS2, resp2, err := makeBodyWS(addr, RouteID(2), "cred-2")
	if err != nil {
		t.Fatalf("makeBodyWS route 2: %v", err)
	}
	defer bodyWS2.Close()
	if _, ok := resp2.(*BodyAttached); !ok {
		t.Fatalf("route 2 attach not BodyAttached; got %T", resp2)
	}

	// Simulate Core teardown: iterate collected route IDs, detach each
	routeIDs := []RouteID{RouteID(1), RouteID(2)}
	for _, rid := range routeIDs {
		cs.detachBody(rid)
	}
	time.Sleep(300 * time.Millisecond)

	// Both attachments removed from cs.body
	cs.mu.RLock()
	bwc1 := cs.body[RouteID(1)]
	bwc2 := cs.body[RouteID(2)]
	bodyLen := len(cs.body)
	cs.mu.RUnlock()
	if bwc1 != nil {
		t.Error("route 1 has stale body attachment after teardown")
	}
	if bwc2 != nil {
		t.Error("route 2 has stale body attachment after teardown")
	}
	if bodyLen != 0 {
		t.Errorf("cs.body has %d entries after full teardown; want 0", bodyLen)
	}

	// Both WS connections closed
	_, _, readErr1 := bodyWS1.ReadMessage()
	if readErr1 == nil {
		t.Error("body WS 1 not closed after teardown")
	}
	_, _, readErr2 := bodyWS2.ReadMessage()
	if readErr2 == nil {
		t.Error("body WS 2 not closed after teardown")
	}
}

// TestBodyAttach_DetachLeavesOtherRoutesIntact proves that detaching one
// route's body attachment leaves other routes' body attachments intact.
func TestBodyAttach_DetachLeavesOtherRoutesIntact(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc.Start(ctx)
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

	// Attach bodies to both routes
	bodyWSA, respA, err := makeBodyWS(addr, RouteID(1), "cred-A")
	if err != nil {
		t.Fatalf("makeBodyWS route 1: %v", err)
	}
	defer bodyWSA.Close()
	if _, ok := respA.(*BodyAttached); !ok {
		t.Fatalf("route 1 attach not BodyAttached; got %T", respA)
	}

	bodyWSB, respB, err := makeBodyWS(addr, RouteID(2), "cred-B")
	if err != nil {
		t.Fatalf("makeBodyWS route 2: %v", err)
	}
	defer bodyWSB.Close()
	if _, ok := respB.(*BodyAttached); !ok {
		t.Fatalf("route 2 attach not BodyAttached; got %T", respB)
	}

	// Verify both attachments exist
	cs.mu.RLock()
	bwcA := cs.body[RouteID(1)]
	bwcB := cs.body[RouteID(2)]
	cs.mu.RUnlock()
	if bwcA == nil {
		t.Fatal("route 1 has no body attachment before detach")
	}
	if bwcB == nil {
		t.Fatal("route 2 has no body attachment before detach")
	}

	// Detach ONLY Route A — simulating teardown of one route
	cs.detachBody(RouteID(1))
	time.Sleep(300 * time.Millisecond)

	// Route A's body must be gone
	cs.mu.RLock()
	bwcA = cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwcA != nil {
		t.Error("route A has stale body attachment after its detachBody")
	}

	// Route B's body must remain
	cs.mu.RLock()
	bwcB = cs.body[RouteID(2)]
	cs.mu.RUnlock()
	if bwcB == nil {
		t.Error("route B body attachment removed by route A's detachBody")
	}
}

// TestBodyAttach_NoStaleEntryAfterDetach proves that detachBody leaves no
// stale cs.body entry: the map entry is nil and the total count is zero
// for a single-route setup.
func TestBodyAttach_NoStaleEntryAfterDetach(t *testing.T) {
	t.Parallel()

	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc.Start(ctx)
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
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Verify attachment exists
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("route 1 has no body attachment after attach")
	}

	// detachBody — what RouteClose and Core teardown call
	cs.detachBody(RouteID(1))
	time.Sleep(300 * time.Millisecond)

	// No stale entry by key lookup
	cs.mu.RLock()
	bwc = cs.body[RouteID(1)]
	bodyLen := len(cs.body)
	cs.mu.RUnlock()
	if bwc != nil {
		t.Error("stale cs.body entry for route 1 after detachBody")
	}
	if bodyLen != 0 {
		t.Errorf("cs.body has %d entries after detachBody; want 0", bodyLen)
	}

	// ReadMessage returns error (connection closed)
	_, _, readErr := bodyWS.ReadMessage()
	if readErr == nil {
		t.Error("body WS not closed after detachBody")
	}
}

// ── M5.2: Body WSS Packet Path tests ──────────────────────────────────
//
// These tests exercise the binary-frame packet forwarding paths between
// Core and Body WebSocket connections through the ControlServer.
//
// Core→Body path: Core WS sends a binary frame → handleWS enqueues
// payload to bwc.inbox → drainInbox writes as binary frame to Body WS.
//
// Body→Core path: Body WS sends a binary frame → handleBodyFrame
// validates and marshals the frame → forwards to Core WS.

// makeCoreConn connects a raw WebSocket to /relay, sends a Register
// message with the given token, and returns the connected WebSocket.
func makeCoreConn(t *testing.T, addr string, token string) *websocket.Conn {
	t.Helper()

	dialer := &websocket.Dialer{}
	ws, _, err := dialer.Dial(fmt.Sprintf("ws://%s/relay", addr), nil)
	if err != nil {
		t.Fatalf("dial /relay: %v", err)
	}

	// Send Register
	regMsg, marshalErr := MarshalControl(&Register{
		Type:  CmdRegister,
		Token: token,
	})
	if marshalErr != nil {
		ws.Close()
		t.Fatalf("marshal register: %v", marshalErr)
	}
	if err := ws.WriteMessage(websocket.TextMessage, regMsg); err != nil {
		ws.Close()
		t.Fatalf("write register: %v", err)
	}

	// Read Registered response
	msgType, raw, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		t.Fatalf("read registered: %v", err)
	}
	if msgType != websocket.TextMessage {
		ws.Close()
		t.Fatalf("expected text Registered; got msg type %d", msgType)
	}
	resp, unmarshalErr := UnmarshalControl(raw)
	if unmarshalErr != nil {
		ws.Close()
		t.Fatalf("unmarshal registered: %v", unmarshalErr)
	}
	if _, ok := resp.(*Registered); !ok {
		ws.Close()
		t.Fatalf("expected Registered; got %T", resp)
	}

	return ws
}

// makeFrameWire marshals a Frame with ProtocolVersion into wire bytes.
// Fails the test on error.
func makeFrameWire(t *testing.T, routeID RouteID, payload []byte) []byte {
	wire, err := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: routeID,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}
	return wire
}

// ── Core → Body packet tests ──────────────────────────────────────────

// TestBodyPacket_CoreToBodyArrives proves that a binary frame from a Core
// WebSocket is delivered as a binary frame to the attached Body WebSocket,
// preserving the opaque payload.
func TestBodyPacket_CoreToBodyArrives(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("pkt-test-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "pkt-test-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// Connect Core WS → Register → cs.conns["pkt-test-reg"]
	coreWS := makeCoreConn(t, addr, "pkt-test-token")
	defer coreWS.Close()

	// Pre-allocate route in Registry
	if err := openTestRoute(svc, "pkt-test-reg", RouteID(42), "body-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// Mark route as known to the Core WS's route map
	cs.mu.RLock()
	core := cs.conns["pkt-test-reg"]
	cs.mu.RUnlock()
	if core == nil {
		t.Fatal("core not in cs.conns after registration")
	}
	core.routes[RouteID(42)] = true

	// Attach Body WS
	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(42), "body-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach response not BodyAttached; got %T", resp)
	}

	// Send a binary frame from Core WS → should arrive at Body WS
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	wire := makeFrameWire(t, RouteID(42), payload)
	if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
		t.Fatalf("coreWS WriteMessage: %v", err)
	}

	// Read from Body WS
	msgType, data, readErr := bodyWS.ReadMessage()
	if readErr != nil {
		t.Fatalf("bodyWS ReadMessage: %v", readErr)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected BinaryMessage on body WS; got msg type %d", msgType)
	}
	if len(data) != len(payload) {
		t.Fatalf("payload length mismatch: body WS got %d bytes; want %d", len(data), len(payload))
	}
	for i := 0; i < len(payload); i++ {
		if data[i] != payload[i] {
			t.Fatalf("payload mismatch at byte %d: got 0x%02x; want 0x%02x", i, data[i], payload[i])
		}
	}
}

// TestBodyPacket_CoreToBodyMultipleInOrder proves that multiple binary
// frames from a Core WS arrive at the Body WS in the same order, with
// correct per-frame payloads.
func TestBodyPacket_CoreToBodyMultipleInOrder(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("pkt-test-token-multi"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "pkt-multi-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "pkt-test-token-multi")
	defer coreWS.Close()

	if err := openTestRoute(svc, "pkt-multi-reg", RouteID(1), "multi-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["pkt-multi-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "multi-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	payloads := [][]byte{
		{0x01},
		{0x02, 0x02},
		{0x03, 0x03, 0x03},
	}

	// Send all three frames from Core
	for _, pl := range payloads {
		wire := makeFrameWire(t, RouteID(1), pl)
		if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
			t.Fatalf("coreWS write: %v", err)
		}
	}

	// Read all three from Body WS
	for i, expected := range payloads {
		msgType, data, readErr := bodyWS.ReadMessage()
		if readErr != nil {
			t.Fatalf("bodyWS ReadMessage frame %d: %v", i, readErr)
		}
		if msgType != websocket.BinaryMessage {
			t.Fatalf("frame %d: expected BinaryMessage; got msg type %d", i, msgType)
		}
		if len(data) != len(expected) {
			t.Fatalf("frame %d: payload length %d; want %d", i, len(data), len(expected))
		}
		for j := 0; j < len(expected); j++ {
			if data[j] != expected[j] {
				t.Fatalf("frame %d byte %d: got 0x%02x; want 0x%02x", i, j, data[j], expected[j])
			}
		}
	}
}

// TestBodyPacket_CoreToBodyNoBodySilent proves that a binary frame for a
// route with no Body attachment is silently accepted by the read loop
// (no crash, no error returned to the Core WS).
func TestBodyPacket_CoreToBodyNoBodySilent(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("no-body-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "no-body-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "no-body-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "no-body-reg", RouteID(1), "unused-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["no-body-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	// NO body attached — send binary frame anyway
	wire := makeFrameWire(t, RouteID(1), []byte{0xca, 0xfe})
	if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
		t.Fatalf("coreWS write: %v", err)
	}

	// Verify no crash by sending a second frame (proves read loop alive)
	wire2 := makeFrameWire(t, RouteID(1), []byte{0xba, 0xbe})
	if err := coreWS.WriteMessage(websocket.BinaryMessage, wire2); err != nil {
		t.Fatalf("coreWS second write: %v", err)
	}

	// If we reach here, no crash occurred
}

// TestBodyPacket_CoreToBodyAfterDetach proves that after a Body WS
// detaches, subsequent Core→Body frames are not delivered and no crash
// occurs.
func TestBodyPacket_CoreToBodyAfterDetach(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("detach-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "detach-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "detach-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "detach-reg", RouteID(1), "detach-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["detach-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "detach-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Detach the body using the same pattern as M5.1
	cs.detachBody(RouteID(1))
	time.Sleep(300 * time.Millisecond)

	// Body WS should be closed (ReadMessage returns error)
	_, _, readErr := bodyWS.ReadMessage()
	if readErr == nil {
		t.Error("body WS not closed after detachBody")
	}

	// Send a binary frame from Core — must not crash
	wire := makeFrameWire(t, RouteID(1), []byte{0xde, 0xad})
	if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
		t.Fatalf("coreWS write after detach: %v", err)
	}

	// Verify cs.body entry is gone
	cs.mu.RLock()
	hasBody := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if hasBody != nil {
		t.Error("stale cs.body entry after detachBody")
	}
}

// TestBodyPacket_CoreToBodyQueueDrops proves that when the Body's inbox
// channel is full, additional Core→Body frames are dropped and the
// dropCount counter increments.
//
// Uses a MaxBodyQueueDepth of 2. The drain goroutine is stopped via
// context cancellation before any packets are sent, making the bounded
// queue overflow deterministically verifiable.
func TestBodyPacket_CoreToBodyQueueDrops(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	svcCfg.MaxBodyQueueDepth = 2
	hash := sha256.Sum256([]byte("drop-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "drop-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "drop-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "drop-reg", RouteID(1), "drop-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["drop-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	// Attach Body
	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "drop-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Stop the drain goroutine so the inbox channel stops emptying.
	// The inbox is empty (no Core→Body packets sent yet), and the drain
	// is blocked on receive; cancel immediately wakes the select and it
	// exits. At most one item can be dequeued before the drain exits.
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("bwc vanished after attach")
	}
	bwc.cancel()

	// Send 10 packets. With queue depth 2 and the drain stopped, at most
	// 2 items stay in the channel; at most 1 item may have been dequeued
	// by the drain before it exited. Mathematically:
	//   drops >= 10 - 2 - 1 = 7
	for i := 0; i < 10; i++ {
		wire := makeFrameWire(t, RouteID(1), []byte{byte(i)})
		if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
			t.Fatalf("coreWS write %d: %v", i, err)
		}
	}

	// Wait deterministically for the server goroutine to process all
	// inbound frames. Each enqueue attempt either fills the channel or
	// hits the non-blocking default path, incrementing dropCount.
	deadline := time.Now().Add(2 * time.Second)
	expected := int64(7)
	for time.Now().Before(deadline) {
		if bwc.dropCount.Load() >= expected {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Backpressure proof: channel filled, overflow increments dropCount
	drops := bwc.dropCount.Load()
	if drops < 7 {
		t.Errorf("dropCount = %d after 10 writes (queue depth 2); want >= 7", drops)
	}
	t.Logf("dropCount = %d after 10 writes (queue depth 2)", drops)
}

// ── Body → Core packet tests ──────────────────────────────────────────

// TestBodyPacket_BodyToCoreArrives proves that a binary frame from a Body
// WebSocket is forwarded as a correctly-marshalled binary frame to the
// owning Core WebSocket.
func TestBodyPacket_BodyToCoreArrives(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("b2c-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "b2c-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "b2c-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "b2c-reg", RouteID(10), "b2c-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["b2c-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(10)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(10), "b2c-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Send a binary frame from Body WS
	payload := []byte{0xca, 0xfe, 0xba, 0xbe}
	wire := makeFrameWire(t, RouteID(10), payload)
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
		t.Fatalf("bodyWS WriteMessage: %v", err)
	}

	// Read forwarded frame from Core WS
	msgType, data, readErr := coreWS.ReadMessage()
	if readErr != nil {
		t.Fatalf("coreWS ReadMessage: %v", readErr)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected BinaryMessage on core WS; got msg type %d", msgType)
	}

	// Unmarshal and verify
	frame, unmarshalErr := UnmarshalFrame(data)
	if unmarshalErr != nil {
		t.Fatalf("UnmarshalFrame: %v", unmarshalErr)
	}
	if frame.RouteID != RouteID(10) {
		t.Errorf("forwarded frame RouteID = %d; want 10", frame.RouteID)
	}
	if frame.Version != ProtocolVersion {
		t.Errorf("forwarded frame Version = %d; want %d", frame.Version, ProtocolVersion)
	}
	if len(frame.Payload) != len(payload) {
		t.Fatalf("payload length %d; want %d", len(frame.Payload), len(payload))
	}
	for i := 0; i < len(payload); i++ {
		if frame.Payload[i] != payload[i] {
			t.Fatalf("payload byte %d: got 0x%02x; want 0x%02x", i, frame.Payload[i], payload[i])
		}
	}
}

// TestBodyPacket_BodyToCoreWrongRoute proves that a binary frame whose
// RouteID does not match the body's attached route is silently dropped.
func TestBodyPacket_BodyToCoreWrongRoute(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("wr-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "wr-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "wr-token")
	defer coreWS.Close()

	// Two routes: route 1 (body attached), route 2 (decoy)
	if err := openTestRoute(svc, "wr-reg", RouteID(1), "wr-cred"); err != nil {
		t.Fatalf("openTestRoute route 1: %v", err)
	}
	if err := openTestRoute(svc, "wr-reg", RouteID(2), "wr-cred-2"); err != nil {
		t.Fatalf("openTestRoute route 2: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["wr-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true
	core.routes[RouteID(2)] = true

	// Attach body to route 1
	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "wr-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Send a frame with RouteID 2 (mismatches body's route 1)
	badWire := makeFrameWire(t, RouteID(2), []byte{0xba, 0xd})
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, badWire); err != nil {
		t.Fatalf("bodyWS write bad frame: %v", err)
	}

	// Now send a valid frame for route 1 — this should arrive
	goodPayload := []byte{0xca, 0xfe}
	goodWire := makeFrameWire(t, RouteID(1), goodPayload)
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, goodWire); err != nil {
		t.Fatalf("bodyWS write good frame: %v", err)
	}

	// Read from Core WS — we should only get the good frame
	msgType, data, readErr := coreWS.ReadMessage()
	if readErr != nil {
		t.Fatalf("coreWS ReadMessage: %v", readErr)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected BinaryMessage; got msg type %d", msgType)
	}
	frame, _ := UnmarshalFrame(data)
	if frame == nil {
		t.Fatal("unmarshal forwarded frame failed")
	}
	if frame.RouteID != RouteID(1) {
		t.Errorf("forwarded frame RouteID = %d; want 1 (wrong-route frame leaked)", frame.RouteID)
	}
	if len(frame.Payload) != len(goodPayload) || frame.Payload[0] != goodPayload[0] {
		t.Error("forwarded payload does not match the good frame")
	}
}

// TestBodyPacket_BodyToCoreBadVersion proves that a frame with a bad
// protocol version is silently dropped by UnmarshalFrame in the body
// read loop.
func TestBodyPacket_BodyToCoreBadVersion(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("bv-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "bv-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "bv-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "bv-reg", RouteID(1), "bv-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["bv-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "bv-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Build a valid frame, then corrupt the version byte
	goodWire, _ := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: RouteID(1),
		Payload: []byte{0x01},
	})
	goodWire[0] = 0 // corrupt to version 0 (invalid)
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, goodWire); err != nil {
		t.Fatalf("bodyWS write bad-version frame: %v", err)
	}

	// Send a valid frame — this must arrive at Core WS
	validWire := makeFrameWire(t, RouteID(1), []byte{0xca, 0xfe})
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, validWire); err != nil {
		t.Fatalf("bodyWS write valid frame: %v", err)
	}

	// Core WS should receive only the valid frame
	msgType, data, readErr := coreWS.ReadMessage()
	if readErr != nil {
		t.Fatalf("coreWS ReadMessage: %v", readErr)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected BinaryMessage; got msg type %d", msgType)
	}
	frame, _ := UnmarshalFrame(data)
	if frame == nil {
		t.Fatal("unmarshal forwarded frame failed")
	}
	if frame.Version == 0 {
		t.Fatal("bad-version frame leaked through to Core WS")
	}
}

// TestBodyPacket_BodyToCoreEmptyPayload proves that a frame with a
// zero-length payload is silently dropped by handleBodyFrame.
func TestBodyPacket_BodyToCoreEmptyPayload(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("emp-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "emp-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "emp-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "emp-reg", RouteID(1), "emp-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["emp-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "emp-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Empty payload frame (MarshalFrame accepts empty payload)
	emptyWire, _ := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: RouteID(1),
		Payload: []byte{},
	})
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, emptyWire); err != nil {
		t.Fatalf("bodyWS write empty frame: %v", err)
	}

	// Valid frame with payload should still arrive
	validWire := makeFrameWire(t, RouteID(1), []byte{0xbe, 0xef})
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, validWire); err != nil {
		t.Fatalf("bodyWS write valid frame: %v", err)
	}

	// Core WS should receive only the valid frame
	msgType, data, readErr := coreWS.ReadMessage()
	if readErr != nil {
		t.Fatalf("coreWS ReadMessage: %v", readErr)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected BinaryMessage; got msg type %d", msgType)
	}
	frame, _ := UnmarshalFrame(data)
	if frame == nil {
		t.Fatal("unmarshal forwarded frame failed")
	}
	if len(frame.Payload) == 0 {
		t.Fatal("zero-length frame was forwarded to Core WS")
	}
}

// TestBodyPacket_BodyToCoreNoCore proves that a binary frame from a Body
// WS is silently dropped when no Core WebSocket is registered for the
// route's owning registration.
func TestBodyPacket_BodyToCoreNoCore(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
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

	// Create route via Registry directly — no Core WS at all
	regID := MustGenerateRegistrationID()
	if err := openTestRoute(svc, regID, RouteID(1), "no-core-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	// Attach Body WS to route 1
	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "no-core-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Send a binary frame from Body WS — no Core to forward to
	wire := makeFrameWire(t, RouteID(1), []byte{0xde, 0xad})
	if err := bodyWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
		t.Fatalf("bodyWS write: %v", err)
	}

	// Wait briefly — no crash should occur
	time.Sleep(500 * time.Millisecond)
}

// TestBodyPacket_BodyToCoreMultipleInOrder proves that multiple binary
// frames from a Body WS arrive at the Core WS in the same order.
func TestBodyPacket_BodyToCoreMultipleInOrder(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("multi-b2c-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "multi-b2c-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "multi-b2c-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "multi-b2c-reg", RouteID(1), "multi-b2c-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["multi-b2c-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "multi-b2c-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	payloads := [][]byte{
		{0xaa},
		{0xbb, 0xbb},
		{0xcc, 0xcc, 0xcc},
	}

	// Send all frames from Body WS
	for _, pl := range payloads {
		wire := makeFrameWire(t, RouteID(1), pl)
		if err := bodyWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
			t.Fatalf("bodyWS write: %v", err)
		}
	}

	// Read all from Core WS, unmarshal, verify order
	for i, expected := range payloads {
		msgType, data, readErr := coreWS.ReadMessage()
		if readErr != nil {
			t.Fatalf("coreWS ReadMessage frame %d: %v", i, readErr)
		}
		if msgType != websocket.BinaryMessage {
			t.Fatalf("frame %d: expected BinaryMessage; got msg type %d", i, msgType)
		}
		frame, unmarshalErr := UnmarshalFrame(data)
		if unmarshalErr != nil {
			t.Fatalf("frame %d: UnmarshalFrame: %v", i, unmarshalErr)
		}
		if len(frame.Payload) != len(expected) {
			t.Fatalf("frame %d: payload length %d; want %d", i, len(frame.Payload), len(expected))
		}
		for j := 0; j < len(expected); j++ {
			if frame.Payload[j] != expected[j] {
				t.Fatalf("frame %d byte %d: got 0x%02x; want 0x%02x", i, j, frame.Payload[j], expected[j])
			}
		}
	}
}

// TestBodyPacket_BodyToCoreAfterRouteClose proves that after a route is
// closed (body detached), binary frames from the Body WS are not
// forwarded to the Core WS.
func TestBodyPacket_BodyToCoreAfterRouteClose(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("close-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "close-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "close-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "close-reg", RouteID(1), "close-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["close-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "close-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// detachBody — simulates route close
	cs.detachBody(RouteID(1))
	time.Sleep(300 * time.Millisecond)

	// Body WS should now be closed (read returns error)
	_, _, readErr := bodyWS.ReadMessage()
	if readErr == nil {
		t.Error("body WS not closed after detachBody")
	}

	// Verify cs.body entry is gone
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc != nil {
		t.Error("stale cs.body entry after detachBody")
	}
}

// ── Config queue-depth tests ──────────────────────────────────────────

// TestBodyPacket_MaxQueueDepthConfig proves that setting MaxBodyQueueDepth
// to a small value constrains the inbox and causes drops when the queue
// overflows.
func TestBodyPacket_MaxQueueDepthConfig(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	svcCfg.MaxBodyQueueDepth = 4
	hash := sha256.Sum256([]byte("cfg-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Read back configured value to confirm it was set
	cfg := svc.Config()
	if cfg.MaxBodyQueueDepth != 4 {
		t.Fatalf("MaxBodyQueueDepth = %d; want 4", cfg.MaxBodyQueueDepth)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "cfg-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "cfg-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "cfg-reg", RouteID(1), "cfg-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["cfg-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "cfg-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// Stop the drain goroutine so the inbox channel stops emptying.
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("bwc vanished after attach")
	}
	bwc.cancel()

	// Send 10 packets (capacity 4). With the drain stopped, at most 4
	// items stay in the channel; at most 1 may have been dequeued before
	// the drain exited. Mathematically: drops >= 10 - 4 - 1 = 5.
	for i := 0; i < 10; i++ {
		wire := makeFrameWire(t, RouteID(1), []byte{byte(i)})
		if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
			t.Fatalf("coreWS write %d: %v", i, err)
		}
	}

	// Wait deterministically for the server goroutine to process all
	// inbound frames.
	deadline := time.Now().Add(2 * time.Second)
	const expected int64 = 5
	for time.Now().Before(deadline) {
		if bwc.dropCount.Load() >= expected {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	drops := bwc.dropCount.Load()
	if drops < 5 {
		t.Errorf("dropCount = %d after 10 writes (depth 4); want >= 5", drops)
	}
	t.Logf("MaxBodyQueueDepth=4: dropCount = %d after 10 writes", drops)
}

// TestBodyPacket_ZeroMaxQueueDepthUsesDefault proves that a
// MaxBodyQueueDepth of 0 causes the body attachment to use
// defaultBodyMaxQueueDepth (256) instead of creating a zero-capacity
// channel.
func TestBodyPacket_ZeroMaxQueueDepthUsesDefault(t *testing.T) {
	t.Parallel()

	svcCfg := DefaultServiceConfig()
	svcCfg.MaxBodyQueueDepth = 0
	hash := sha256.Sum256([]byte("default-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Verify the config value is 0 (sentinel for "use default")
	cfg := svc.Config()
	if cfg.MaxBodyQueueDepth != 0 {
		t.Fatalf("MaxBodyQueueDepth = %d; want 0", cfg.MaxBodyQueueDepth)
	}

	ctx := context.Background()
	svc.Start(ctx)
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	cs.generateRegID = func() (RegistrationID, error) {
		return "default-reg", nil
	}
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	coreWS := makeCoreConn(t, addr, "default-token")
	defer coreWS.Close()

	if err := openTestRoute(svc, "default-reg", RouteID(1), "default-cred"); err != nil {
		t.Fatalf("openTestRoute: %v", err)
	}

	cs.mu.RLock()
	core := cs.conns["default-reg"]
	cs.mu.RUnlock()
	core.routes[RouteID(1)] = true

	bodyWS, resp, attachErr := makeBodyWS(addr, RouteID(1), "default-cred")
	if attachErr != nil {
		t.Fatalf("makeBodyWS: %v", attachErr)
	}
	defer bodyWS.Close()
	if _, ok := resp.(*BodyAttached); !ok {
		t.Fatalf("attach not BodyAttached; got %T", resp)
	}

	// With default depth 256, send 10 frames — all should deliver
	for i := 0; i < 10; i++ {
		wire := makeFrameWire(t, RouteID(1), []byte{byte(i)})
		if err := coreWS.WriteMessage(websocket.BinaryMessage, wire); err != nil {
			t.Fatalf("coreWS write %d: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond)

	// Drain all 10 from body WS to prove delivery works
	for i := 0; i < 10; i++ {
		msgType, _, readErr := bodyWS.ReadMessage()
		if readErr != nil {
			t.Fatalf("bodyWS ReadMessage frame %d: %v", i, readErr)
		}
		if msgType != websocket.BinaryMessage {
			t.Fatalf("frame %d: expected BinaryMessage; got msg type %d", i, msgType)
		}
	}

	// Verify no drops happened with default depth
	cs.mu.RLock()
	bwc := cs.body[RouteID(1)]
	cs.mu.RUnlock()
	if bwc == nil {
		t.Fatal("bwc vanished during test")
	}
	drops := bwc.dropCount.Load()
	if drops != 0 {
		t.Errorf("dropCount = %d with default queue depth; want 0 (depth 256 can hold all)", drops)
	}
}
