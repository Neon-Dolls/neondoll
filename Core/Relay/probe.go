// SPDX-License-Identifier: AGPL-3.0-only
package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Probe magic constants (ASCII).  Used as the first 4 bytes of every
// probe datagram to distinguish probe messages from other UDP traffic on
// the probe endpoint — although the probe port is dedicated, the magic
// provides an additional guard against stray datagrams.
const (
	ProbeMagicReq  = "NLPR"           // Non-Disruptive Path Probe Request
	ProbeMagicResp = "NLPS"           // Non-Disruptive Path Probe Response
	ProbeNonceSize = 8               // bytes
	ProbeHMACSize  = 32              // HMAC-SHA256
	ProbeDatagramSize = 4 +          // magic
		8 +          // route ID
		ProbeNonceSize + // nonce
		ProbeHMACSize  // HMAC
)

// ProbeDatagramSize == 52 bytes total.

// Defaults for ProbeListenerConfig.
const (
	DefaultProbePort    = 0 // dynamically allocated if 0
	DefaultProbeRateLim = 100 // probes/second per source IP
	DefaultProbeTimeout = 5 * time.Second
)

// ProbeListenerConfig configures a relay's probe listener.
type ProbeListenerConfig struct {
	Enabled  bool          // set to true to start the probe listener
	Port     int           // 0 = pick ephemeral port
	RateLimit int          // max probes/second per source IP; 0 = default
}

// ApplyDefaults fills zero fields with sensible defaults.
func (c *ProbeListenerConfig) ApplyDefaults() {
	if c.Port == 0 {
		c.Port = DefaultProbePort
	}
	if c.RateLimit == 0 {
		c.RateLimit = DefaultProbeRateLim
	}
}

// ProbeListener accepts non-WG reachability probes on a dedicated UDP
// port.  Each probe request is HMAC-authenticated using the route's
// credentials token, proving the body holds a valid route credential
// without exchanging a WireGuard handshake or creating a tunnel.
//
// The probe port is architecturally independent of the relay's WG data
// path — it neither inspects nor forwards to the opaque datagram pipeline.
type ProbeListener struct {
	config ProbeListenerConfig
	reg    *Registry
	conn   *net.UDPConn
	log    *slog.Logger

	mu        sync.Mutex
	rateLimit map[string]time.Time // IP → last probe time

	stopped atomic.Bool
	doneCh  chan struct{}
}

// NewProbeListener creates a probe listener wired to the given registry.
func NewProbeListener(cfg ProbeListenerConfig, reg *Registry, log *slog.Logger) *ProbeListener {
	cfg.ApplyDefaults()
	return &ProbeListener{
		config:    cfg,
		reg:       reg,
		log:       log.With("component", "probe-listener"),
		rateLimit: make(map[string]time.Time),
		doneCh:    make(chan struct{}),
	}
}

// Start binds the UDP socket and begins accepting probe requests.
func (pl *ProbeListener) Start() error {
	addr := &net.UDPAddr{Port: pl.config.Port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("probe listener: bind port %d: %w", pl.config.Port, err)
	}
	pl.conn = conn
	go pl.runLoop()
	pl.log.Info("probe listener ready", slog.Int("port", conn.LocalAddr().(*net.UDPAddr).Port))
	return nil
}

// Addr returns the listening address of the probe socket.
func (pl *ProbeListener) Addr() *net.UDPAddr {
	if pl.conn == nil {
		return nil
	}
	return pl.conn.LocalAddr().(*net.UDPAddr)
}

// Stop closes the probe socket and waits for runLoop to exit.
// Safe to call multiple times; safe to call before Start (returns
// immediately without blocking).
func (pl *ProbeListener) Stop() {
	pl.stopped.Store(true)
	if pl.conn == nil {
		return // never started; nothing to wait on
	}
	pl.conn.Close()
	<-pl.doneCh
}

// runLoop reads probe datagrams and dispatches them to handleProbe.
func (pl *ProbeListener) runLoop() {
	defer close(pl.doneCh)

	buf := make([]byte, ProbeDatagramSize+64) // 64 bytes headroom
	for !pl.stopped.Load() {
		n, src, err := pl.conn.ReadFromUDP(buf)
		if err != nil {
			if pl.stopped.Load() {
				return
			}
			pl.log.Warn("probe listener read error", "error", err)
			continue
		}

		if n != ProbeDatagramSize {
			pl.log.Debug("probe datagram size mismatch", "got", n, "want", ProbeDatagramSize)
			continue
		}

		go pl.handleProbe(buf[:n], src)
	}
}

// handleProbe processes a single probe request and sends a response.
func (pl *ProbeListener) handleProbe(data []byte, src *net.UDPAddr) {
	// Copy the datagram body (we're in a goroutine and buf gets reused).
	body := make([]byte, len(data))
	copy(body, data)

	// Rate limiting by source IP.
	pl.mu.Lock()
	last, ok := pl.rateLimit[src.IP.String()]
	now := time.Now()
	if ok && now.Sub(last) < time.Second/time.Duration(pl.config.RateLimit) {
		pl.mu.Unlock()
		pl.log.Debug("probe rate limited", "src", src)
		return
	}
	pl.rateLimit[src.IP.String()] = now
	pl.mu.Unlock()

	// Magic check.
	if string(body[:4]) != ProbeMagicReq {
		pl.log.Debug("probe bad magic", "got", fmt.Sprintf("%x", body[:4]))
		return
	}

	// Extract route ID and nonce.
	routeID := RouteID(binary.LittleEndian.Uint64(body[4:12]))
	nonce := body[12:20]

	// Look up route.
	entry, ok := pl.reg.Route(routeID)
	if !ok {
		pl.log.Debug("probe route not found", "route_id", routeID)
		return
	}

	// Verify HMAC.
	mac := hmac.New(sha256.New, []byte(entry.Credentials.Token))
	mac.Write([]byte(ProbeMagicReq))
	var routeIDBytes [8]byte
	binary.LittleEndian.PutUint64(routeIDBytes[:], uint64(routeID))
	mac.Write(routeIDBytes[:])
	mac.Write(nonce)
	expectedMAC := mac.Sum(nil)

	if !hmac.Equal(body[20:52], expectedMAC) {
		pl.log.Debug("probe HMAC mismatch", "route_id", routeID, "src", src)
		return
	}

	// Build response.
	macResp := hmac.New(sha256.New, []byte(entry.Credentials.Token))
	macResp.Write([]byte(ProbeMagicResp))
	macResp.Write(routeIDBytes[:])
	macResp.Write(nonce)
	respMAC := macResp.Sum(nil)

	resp := make([]byte, ProbeDatagramSize)
	copy(resp[0:4], ProbeMagicResp)
	copy(resp[4:12], routeIDBytes[:])
	copy(resp[12:20], nonce)
	copy(resp[20:52], respMAC)

	pl.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := pl.conn.WriteToUDP(resp, src); err != nil {
		pl.log.Debug("probe response write error", "error", err)
	}
}

// ProbeHMAC computes the probe request HMAC for a given route ID, nonce,
// and credential token.  Exported for testing and client-side use.
func ProbeHMAC(magic string, routeID RouteID, nonce []byte, token string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(magic))
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(routeID))
	mac.Write(b[:])
	mac.Write(nonce)
	return mac.Sum(nil)
}

// ProbeRequest builds a complete probe request datagram.
func ProbeRequest(routeID RouteID, nonce []byte, token string) []byte {
	buf := make([]byte, ProbeDatagramSize)
	copy(buf[0:4], ProbeMagicReq)
	binary.LittleEndian.PutUint64(buf[4:12], uint64(routeID))
	copy(buf[12:20], nonce)
	hmacVal := ProbeHMAC(ProbeMagicReq, routeID, nonce, token)
	copy(buf[20:52], hmacVal)
	return buf
}

// VerifyProbeResponse checks whether a probe response datagram is valid
// for the given nonce, route ID, and credential token.
func VerifyProbeResponse(data []byte, routeID RouteID, nonce []byte, token string) error {
	if len(data) < ProbeDatagramSize {
		return fmt.Errorf("probe response too short: %d < %d", len(data), ProbeDatagramSize)
	}
	if string(data[:4]) != ProbeMagicResp {
		return errors.New("probe response: bad magic")
	}
	if RouteID(binary.LittleEndian.Uint64(data[4:12])) != routeID {
		return errors.New("probe response: route ID mismatch")
	}
	if string(data[12:20]) != string(nonce) {
		return errors.New("probe response: nonce mismatch")
	}
	expected := ProbeHMAC(ProbeMagicResp, routeID, nonce, token)
	if !hmac.Equal(data[20:52], expected) {
		return errors.New("probe response: HMAC mismatch")
	}
	return nil
}

// GenerateProbeNonce returns a random nonce for a probe request.
func GenerateProbeNonce() ([]byte, error) {
	b := make([]byte, ProbeNonceSize)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("probe nonce: %w", err)
	}
	return b, nil
}