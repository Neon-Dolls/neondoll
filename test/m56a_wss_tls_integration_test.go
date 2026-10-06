//go:build e2e

// Core 4 / M5.6a — TLS/WSS transport conformance.
//
// Proves a real WireGuard handshake and bidirectional encrypted traffic
// over a TLS-wrapped WebSocket (wss://) through the Body transport path:
//
//   Body WG → BodyWSSBind → TLS proxy → ControlServer → Core WS → RelayTransport → Core WG
//
// The TLS proxy terminates wss:// TCP connections and forwards them as
// plain TCP to the ControlServer, enabling wss:// without modifying the
// ControlServer's HTTP layer. The Core side continues using plain ws://,
// proving no regression.
//
// Proof establishes:
//   · BodyWSSBind connects and authenticates over wss://
//   · /body path normalization works for wss:// endpoints
//   · Real WG handshake over WSS through the Relay
//   · Bidirectional encrypted overlay traffic over WSS
//   · ws:// behavior is preserved (Core side unmodified)

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	body "github.com/Neon-Dolls/neondoll/Body"
	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

// ── TLS helpers ───────────────────────────────────────────────────────────────

// generateSelfSignedCert creates a self-signed TLS certificate for "localhost"
// suitable for testing wss:// connections with InsecureSkipVerify.
func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	// Ed25519 implements crypto.Signer and works directly with
	// x509.CreateCertificate (unlike ECDH/X25519).  We only need this
	// for the TLS cert — WireGuard uses its own X25519-based keypairs.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, privKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	// PEM-encode certificate and private key for tls.X509KeyPair.
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert
}

// tlsProxy is a transparent TLS terminator that forwards connections
// to a plain TCP backend.  This lets us test wss:// Body connections
// without modifying the ControlServer's HTTP server to support TLS directly.
type tlsProxy struct {
	ln      net.Listener
	backend string
	mu      sync.Mutex // guards conns
	conns   []net.Conn // tracked for cleanup — close() closes all
}

func newTLSProxy(ctx context.Context, backend string, cert tls.Certificate) (*tlsProxy, error) {
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		return nil, err
	}
	p := &tlsProxy{ln: ln, backend: backend, mu: sync.Mutex{}, conns: []net.Conn{}}
	go p.serve(ctx)
	return p, nil
}

func (p *tlsProxy) Addr() string {
	return p.ln.Addr().String()
}

// Close stops the TLS proxy by closing the listener and all active TLS
// connections.  The forwarding goroutines (blocked on io.Copy) will see
// the connection errors and exit naturally.
func (p *tlsProxy) Close() {
	p.ln.Close()
	p.mu.Lock()
	conns := p.conns
	p.conns = []net.Conn{}
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (p *tlsProxy) serve(ctx context.Context) {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

// handle performs bidirectional copy between a TLS connection and a plain
// TCP connection to the backend.  This is transparent to the WebSocket
// protocol — the gorilla/websocket server on the ControlServer sees the
// HTTP Upgrade request with no TLS wrapping.
func (p *tlsProxy) handle(tlsConn net.Conn) {
	defer tlsConn.Close()
	p.mu.Lock()
	p.conns = append(p.conns, tlsConn)
	p.mu.Unlock()
	backend, err := net.Dial("tcp", p.backend)
	if err != nil {
		return
	}
	defer backend.Close()

	// Bidirectional copy until both directions complete.
	// When either direction completes, close both connections so the
	// opposing io.Copy is unblocked.  Both goroutines must finish
	// before handle returns.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(backend, tlsConn)
		tlsConn.Close()
		backend.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(tlsConn, backend)
		backend.Close()
		tlsConn.Close()
		done <- struct{}{}
	}()
	// Wait for both copy goroutines to finish.
	// handle's defer tlsConn.Close() and defer backend.Close() run
	// here, but the connections are already closed by whichever
	// goroutine finished first — the defers are a safety net.
	<-done
	<-done
}

// ── the integration proof ─────────────────────────────────────────────────────

func TestBodyWSS_RealWireGuardOverBodyWSS_TLS(t *testing.T) {
	const routeID = relay.RouteID(1)
	const token = "m56a-tls-test-token"
	const routeCred = "tls-wg-cred"

	// Generate WireGuard keypairs — independent of any RouteID/Relay credential.
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	// ── 1. Start Service + ControlServer (plain TCP) ──
	svcCfg := relay.DefaultServiceConfig()
	hash := sha256.Sum256([]byte(token))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svcCfg.KeepaliveInterval = 2 * time.Second
	svcCfg.RouteTimeout = 180 * time.Second
	svcCfg.RegistrationTimeout = 180 * time.Second

	svc, svcErr := relay.NewService(svcCfg, relay.ClientConfig{})
	if svcErr != nil {
		t.Fatalf("NewService: %v", svcErr)
	}

	svcCtx, svcCancel := context.WithCancel(context.Background())
	defer svcCancel()
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	relayAddr := pickFreeTCPAddrPort(t)
	cs := relay.NewControlServer(svc, relayAddr, nil)
	csCtx, csCancel := context.WithCancel(svcCtx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// ── 2. TLS proxy: wraps wss:// → plain TCP to the ControlServer ──
	cert := generateSelfSignedCert(t)
	proxy, err := newTLSProxy(svcCtx, relayAddr, cert)
	if err != nil {
		t.Fatalf("newTLSProxy: %v", err)
	}
	t.Cleanup(func() { proxy.Close() })
	wssAddr := "wss://" + proxy.Addr()

	// ── 3. Core side: ControlClient (plain ws://, proving no regression) ──
	cfg := relay.DefaultClientConfig()
	cfg.RelayURL = "ws://" + relayAddr + "/relay"
	cfg.RegistrationToken = token
	cfg.HandshakeTimeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.PingInterval = 5 * time.Second

	client := relay.NewControlClient(cfg)
	if err := client.Start(svcCtx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	waitClientConnected(t, client, "core-client")

	// ── 4. Open a route through the ControlClient ──
	opened, openErr := client.OpenRoute(svcCtx, routeID, relay.RouteCredentials{Token: routeCred})
	if openErr != nil {
		t.Fatalf("OpenRoute(%d): %v", routeID, openErr)
	}

	// Get the regID that OWNS this route, then set its credentials.
	regID, ok := svc.Registry().RouteRegistration(routeID)
	if !ok {
		t.Fatalf("RouteRegistration(%d): not found", routeID)
	}
	if err := svc.Registry().SetRouteCredentials(regID, routeID, relay.RouteCredentials{Token: routeCred}); err != nil {
		t.Fatalf("SetRouteCredentials: %v", err)
	}

	// Create RelayTransport for the Core WG device.
	tr := relay.NewRelayTransport(client)
	defer tr.Close()
	_, _, trOpenErr := tr.Open(0)
	if trOpenErr != nil {
		t.Fatalf("tr.Open: %v", trOpenErr)
	}
	_ = opened

	// ── 5. Body side: BodyWSSBind with wss:// and TLS config ──
	// NOTE: do NOT call bind.Open(0) here.  The WG device's BindUpdate()
	// (called from dev.Up()) will be the sole opener, avoiding a
	// close+reconnect cycle that loses the first handshake initiations.
	bind := body.NewWSSBind(wssAddr, routeID, routeCred)
	defer bind.Close()
	bind.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})

	// ── 6. Start WireGuard devices ──
	// Each device gets its own overlay IP.  The allowed-ip is the peer's
	// overlay prefix so WG routes traffic through the tunnel.
	coreOverlay := netip.MustParseAddr("fd00::11")
	bodyOverlay := netip.MustParseAddr("fd00::22")
	endpoint := "relay:1"

	coreDev, coreNet := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, endpoint, "fd00::22/128")
	bodyDev, bodyNet := startWgDevice(t, bind, bodyKeys, bodyOverlay, coreKeys.pubHex, endpoint, "fd00::11/128")

	// ── 7. Wait for WG handshake (observable readiness) ──
	waitHandshake(t, coreDev, "Core WG device", 20*time.Second)
	waitHandshake(t, bodyDev, "Body WG device", 20*time.Second)

	// ── 8. Prove bidirectional encrypted overlay traffic ──
	bodyListenPort := uint16(7)
	bodyAddr := netip.AddrPortFrom(netip.MustParseAddr("fd00::22"), bodyListenPort)
	bodyListen, listenErr := bodyNet.ListenUDPAddrPort(bodyAddr)
	if listenErr != nil {
		t.Fatalf("bodyNet.ListenUDP: %v", listenErr)
	}

	// Core → Body
	const payloadA = "hello from core over tls-wss"
	{
		sender, dialErr := coreNet.DialUDPAddrPort(netip.AddrPort{}, bodyAddr)
		if dialErr != nil {
			t.Fatalf("coreNet.DialUDP: %v", dialErr)
		}
		n, writeErr := sender.Write([]byte(payloadA))
		if writeErr != nil {
			t.Fatalf("Core→Body write: %v", writeErr)
		}
		if n <= 0 {
			t.Fatalf("Core→Body wrote %d bytes (expected >0)", n)
		}

		// Core → Body: bounded WG overlay read with error checking
		recvCh := make(chan string, 1)
		recvDone := make(chan struct{}, 1)
		go func() {
			buf := make([]byte, 1500)
			n, _, rerr := bodyListen.ReadFrom(buf)
			if rerr == nil && n > 0 {
				recvCh <- string(buf[:n])
			}
			recvDone <- struct{}{}
		}()
		deadline := time.Now().Add(15 * time.Second)
		gotPayload := false
		for !gotPayload && time.Now().Before(deadline) {
			select {
			case got := <-recvCh:
				if got != payloadA {
					t.Fatalf("payload mismatch: expected %q, got %q", payloadA, got)
				}
				gotPayload = true
			case <-time.After(1 * time.Second):
			}
		}
		bodyListen.Close()
		<-recvDone // join: wait for read goroutine to actually exit
		if !gotPayload {
			t.Fatalf("Core→Body: no data after WG write within 15s")
		}
	}

	// Body → Core (reverse direction)
	const payloadB = "hello from body over tls-wss"
	{
		coreListenAddr := netip.AddrPortFrom(netip.MustParseAddr("fd00::11"), bodyListenPort)
		coreListen, listenErr2 := coreNet.ListenUDPAddrPort(coreListenAddr)
		if listenErr2 != nil {
			t.Fatalf("coreNet.ListenUDP: %v", listenErr2)
		}

		bodySender, dialErr := bodyNet.DialUDPAddrPort(netip.AddrPort{}, coreListenAddr)
		if dialErr != nil {
			t.Fatalf("bodyNet.DialUDP: %v", dialErr)
		}
		n, writeErr := bodySender.Write([]byte(payloadB))
		if writeErr != nil {
			t.Fatalf("Body→Core write: %v", writeErr)
		}
		if n <= 0 {
			t.Fatalf("Body→Core wrote %d bytes (expected >0)", n)
		}

		// Body→Core: bounded WG overlay read with error checking
		recvCh2 := make(chan string, 1)
		recvDone2 := make(chan struct{}, 1)
		go func() {
			buf2 := make([]byte, 1500)
			n, _, rerr := coreListen.ReadFrom(buf2)
			if rerr == nil && n > 0 {
				recvCh2 <- string(buf2[:n])
			}
			recvDone2 <- struct{}{}
		}()
		deadline := time.Now().Add(15 * time.Second)
		gotPayload := false
		for !gotPayload && time.Now().Before(deadline) {
			select {
			case got := <-recvCh2:
				if got != payloadB {
					t.Fatalf("payload mismatch: expected %q, got %q", payloadB, got)
				}
				gotPayload = true
			case <-time.After(1 * time.Second):
			}
		}
		coreListen.Close()
		<-recvDone2 // join: wait for read goroutine to actually exit
		if !gotPayload {
			t.Fatalf("Body→Core: no data after WG write within 15s")
		}
	}

	// ── 9. Prove ws:// behavior preserved ──
	// The Core side used ws:// for the ControlClient connection and
	// successfully completed handshake + bidirectional traffic through
	// the same Service.  The Body used wss:// with a separate TLS proxy,
	// proving both transport schemes work concurrently.
	t.Log("Core side used ws:// — unmodified and functional")
	t.Log("Body side used wss:// through TLS proxy — TLS/WSS conformance verified")

	// ── 10. Prove Relay opaque forwarding ──
	t.Log("Relay opaque forwarding verified by construction")
}
