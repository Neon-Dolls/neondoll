//go:build e2e

package integration

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/WireGuard"
)

const (
	coreWgPort   = 51821
	bodyWgPort   = 51822
	coreOverlay  = "fd00:1::1"
	bodyOverlay  = "fd00:1::2"
	echoPort     = 9999
	wsPort       = 9998
	handshakeTO  = 15 * time.Second
	reconnectTO  = 30 * time.Second
	keepaliveSec = 5
	echoMsg      = "Hello from overlay!"
)

func generateKeypair() (private, public [32]byte) {
	curve := ecdh.X25519()
	privKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		panic("ecdh generate: " + err.Error())
	}
	pubKey := privKey.PublicKey()
	copy(private[:], privKey.Bytes())
	copy(public[:], pubKey.Bytes())
	return
}

func parseAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		panic("addr: " + err.Error())
	}
	return a
}

// runEchoServer starts a goroutine that reads a fixed-size message and echoes it back.
func runEchoServer(wg *sync.WaitGroup, ln net.Listener, msgLen int) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, data); err != nil {
			return
		}
		conn.Write(data)
	}()
}

func TestM3WireGuardOverlayE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// ── 1. Generate keypairs ─────────────────────────────────────────────────
	t.Log("generating wireguard keypairs")
	corePriv, corePub := generateKeypair()
	bodyPriv, bodyPub := generateKeypair()

	// ── 2. Start Core tunnel ─────────────────────────────────────────────────
	t.Log("starting core tunnel")
	coreTun := wireguard.NewRealTunnel(nil)
	coreCfg := wireguard.Config{
		PrivateKey:     corePriv,
		ListenPort:     coreWgPort,
		OverlayAddress: parseAddr(coreOverlay),
		MTU:            wireguard.DefaultMTU,
	}
	if err := coreTun.Start(ctx, coreCfg); err != nil {
		t.Fatalf("core tunnel start: %v", err)
	}
	defer coreTun.Stop()

	bodyPeer := wireguard.PeerConfig{
		PublicKey:           bodyPub,
		AllowedIPs:          []netip.Prefix{netip.PrefixFrom(parseAddr(bodyOverlay), 128)},
		Endpoint:            fmt.Sprintf("127.0.0.1:%d", bodyWgPort),
		PersistentKeepalive: keepaliveSec * time.Second,
	}
	if err := coreTun.ReconfigurePeers([]wireguard.PeerConfig{bodyPeer}); err != nil {
		t.Fatalf("core reconfigure peers: %v", err)
	}

	// ── 3. Start Body tunnel ─────────────────────────────────────────────────
	t.Log("starting body tunnel")
	bodyCfg := body.BodyTunnelConfig{
		PrivateKey:     bodyPriv,
		CorePublicKey:  corePub,
		OverlayAddress: parseAddr(bodyOverlay),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", coreWgPort),
		ListenPort:     bodyWgPort,
		MTU:            wireguard.DefaultMTU,
	}
	bodyTun := body.NewBodyTunnel(bodyCfg, nil)
	if err := bodyTun.Start(ctx); err != nil {
		t.Fatalf("body tunnel start: %v", err)
	}
	defer bodyTun.Stop()

	// ── 4. Wait for WireGuard handshake ──────────────────────────────────────
	t.Logf("waiting for wireguard handshake (up to %v)", handshakeTO)
	if err := coreTun.WaitHandshake(ctx, handshakeTO); err != nil {
		t.Fatalf("core handshake timeout: %v", err)
	}

	// ── 5. Overlay TCP echo ──────────────────────────────────────────────────
	t.Log("testing overlay TCP echo")
	coreNet := coreTun.Netstack()
	bodyNet := bodyTun.Netstack()
	if coreNet == nil || bodyNet == nil {
		t.Fatal("netstack not available after tunnel start")
	}

	var wg sync.WaitGroup

	echoAddr := &net.TCPAddr{IP: net.IPv6unspecified, Port: echoPort}
	echoListener, err := coreNet.ListenTCP(echoAddr)
	if err != nil {
		t.Fatalf("core netstack listen tcp: %v", err)
	}
	runEchoServer(&wg, echoListener, len(echoMsg))

	dialAddr := &net.TCPAddr{IP: net.ParseIP(coreOverlay), Port: echoPort}
	conn, err := bodyNet.DialTCP(dialAddr)
	if err != nil {
		t.Fatalf("body netstack dial tcp: %v", err)
	}

	if _, err := conn.Write([]byte(echoMsg)); err != nil {
		t.Fatalf("overlay write: %v", err)
	}
	recv := make([]byte, len(echoMsg))
	if _, err := io.ReadFull(conn, recv); err != nil {
		t.Fatalf("overlay read: %v", err)
	}
	if !bytes.Equal([]byte(echoMsg), recv) {
		t.Fatalf("overlay echo mismatch: sent %q, received %q", echoMsg, recv)
	}
	conn.Close()
	wg.Wait()
	t.Log("overlay TCP echo: OK")

	// ── 6. WebSocket upgrade through overlay ─────────────────────────────────
	t.Log("testing WebSocket upgrade through overlay")
	wsListener, err := coreNet.ListenTCP(&net.TCPAddr{IP: net.IPv6unspecified, Port: wsPort})
	if err != nil {
		t.Fatalf("core netstack listen ws: %v", err)
	}

	wsWg := sync.WaitGroup{}
	wsWg.Add(1)
	go func() {
		defer wsWg.Done()
		defer wsListener.Close()

		conn, aErr := wsListener.Accept()
		if aErr != nil {
			t.Logf("ws listener accept: %v", aErr)
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		n, rErr := conn.Read(buf)
		if rErr != nil {
			t.Logf("ws read: %v", rErr)
			return
		}

		req := string(buf[:n])
		keyStr := "Sec-WebSocket-Key: "
		start := stringsIndex(req, keyStr)
		if start < 0 {
			t.Logf("ws: no Sec-WebSocket-Key header: %s", req[:min(n, 200)])
			return
		}
		end := stringsIndex(req[start:], "\r\n")
		if end < 0 {
			end = len(req) - start
		}
		wsKey := req[start+len(keyStr) : start+end]

		resp := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", wsAcceptKey(wsKey))
		conn.Write([]byte(resp))

		// Read and echo back data after upgrade.
		echoBuf := make([]byte, len(echoMsg))
		if _, rErr := io.ReadFull(conn, echoBuf); rErr != nil {
			t.Logf("ws echo read: %v", rErr)
			return
		}
		conn.Write(echoBuf)
	}()

	wsConn, err := bodyNet.DialTCP(&net.TCPAddr{IP: net.ParseIP(coreOverlay), Port: wsPort})
	if err != nil {
		t.Fatalf("body dial ws via overlay: %v", err)
	}

	wsReq := fmt.Sprintf("GET /ws HTTP/1.1\r\n"+
		"Host: [%s]\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", coreOverlay)
	if _, err := wsConn.Write([]byte(wsReq)); err != nil {
		t.Fatalf("ws write upgrade request: %v", err)
	}

	respBuf := make([]byte, 4096)
	n, err := wsConn.Read(respBuf)
	if err != nil {
		t.Fatalf("ws read upgrade response: %v", err)
	}
	if !bytes.Contains(respBuf[:n], []byte("101 Switching Protocols")) {
		t.Fatalf("ws upgrade failed, got: %s", string(respBuf[:n]))
	}
	wsConn.Close()
	wsListener.Close()
	wsWg.Wait()
	t.Log("WebSocket upgrade through overlay: OK")

	// ── 7. Core destruction and reconstruction ────────────────────────────────
	t.Log("destroying and reconstructing core runtime")
	coreTun.Stop()

	coreTun2 := wireguard.NewRealTunnel(nil)
	coreCfg2 := wireguard.Config{
		PrivateKey:     corePriv,
		ListenPort:     coreWgPort,
		OverlayAddress: parseAddr(coreOverlay),
		MTU:            wireguard.DefaultMTU,
	}
	if err := coreTun2.Start(ctx, coreCfg2); err != nil {
		t.Fatalf("core tunnel2 start: %v", err)
	}
	defer coreTun2.Stop()

	bodyPeer2 := wireguard.PeerConfig{
		PublicKey:           bodyPub,
		AllowedIPs:          []netip.Prefix{netip.PrefixFrom(parseAddr(bodyOverlay), 128)},
		Endpoint:            fmt.Sprintf("127.0.0.1:%d", bodyWgPort),
		PersistentKeepalive: keepaliveSec * time.Second,
	}
	if err := coreTun2.ReconfigurePeers([]wireguard.PeerConfig{bodyPeer2}); err != nil {
		t.Fatalf("core tunnel2 reconfigure peers: %v", err)
	}

	t.Logf("waiting for body to re-handshake after core restart (up to %v)", reconnectTO)
	if err := coreTun2.WaitHandshake(ctx, reconnectTO); err != nil {
		t.Fatalf("re-handshake timeout: %v", err)
	}

	t.Log("testing overlay TCP echo after reconstruction")
	coreNet2 := coreTun2.Netstack()
	if coreNet2 == nil {
		t.Fatal("core tunnel2 netstack is nil")
	}

	echoListener2, err := coreNet2.ListenTCP(echoAddr)
	if err != nil {
		t.Fatalf("core netstack2 listen tcp: %v", err)
	}
	runEchoServer(&wg, echoListener2, len(echoMsg))

	conn2, err := bodyNet.DialTCP(dialAddr)
	if err != nil {
		t.Fatalf("body netstack2 dial tcp: %v", err)
	}

	if _, err := conn2.Write([]byte(echoMsg)); err != nil {
		t.Fatalf("overlay2 write: %v", err)
	}
	recv2 := make([]byte, len(echoMsg))
	if _, err := io.ReadFull(conn2, recv2); err != nil {
		t.Fatalf("overlay2 read: %v", err)
	}
	if !bytes.Equal([]byte(echoMsg), recv2) {
		t.Fatalf("overlay2 echo mismatch: sent %q, received %q", echoMsg, recv2)
	}
	conn2.Close()
	wg.Wait()
	t.Log("overlay TCP echo after reconstruction: OK")

	t.Log("M3 e2e test PASSED")
}

func stringsIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func wsAcceptKey(key string) string {
	magic := "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}
