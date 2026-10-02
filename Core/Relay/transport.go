// Package relay — Core WireGuard Packet Transport.
//
// M4.5 introduces a conn.Bind implementation that routes encrypted WireGuard
// datagrams through the Core ↔ Relay WSS control tunnel as opaque Frame
// messages instead of raw UDP. Direct M3 UDP operation is preserved when
// RelayTransport is not configured.
//
// RelayTransport only carries already-encrypted WireGuard datagrams. It never
// parses WG packet headers, keys, handshake data, or identity material.
package relay

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// MaxQueuePackets is the maximum number of inbound relay WG packets that
	// can be queued before backpressure rejects new arrivals.
	MaxQueuePackets = 256

	// RelayEndpointPrefix identifies a virtual relay endpoint string.
	RelayEndpointPrefix = "relay:"
)

var (
	ErrTransportClosed = errors.New("relay: transport is closed")
	ErrQueueFull       = errors.New("relay: inbound packet queue is full")
	ErrEndpointFormat  = errors.New("relay: invalid relay endpoint format")
	ErrEndpointType    = errors.New("relay: endpoint is not a RelayEndpoint")
)

// ── PacketQueue ──────────────────────────────────────────────────────────────

// PacketQueue is a bounded FIFO of opaque WG datagrams received from the Relay.
// Push returns an error when full (deterministic backpressure). Pop blocks until
// a packet is available or the queue is closed.
type queuedPacket struct {
	routeID RouteID
	payload []byte
}

type PacketQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	packets []queuedPacket
	max     int
	closed  bool
}

// NewPacketQueue creates a queue with the given maximum capacity.
func NewPacketQueue(max int) *PacketQueue {
	q := &PacketQueue{
		mu:      sync.Mutex{},
		packets: make([]queuedPacket, 0),
		max:     max,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push adds a packet to the queue. Returns ErrQueueFull if at capacity.
// Returns ErrTransportClosed after Close().
func (q *PacketQueue) Push(routeID RouteID, packet []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrTransportClosed
	}
	if len(q.packets) >= q.max {
		return ErrQueueFull
	}
	q.packets = append(q.packets, queuedPacket{routeID: routeID, payload: packet})
	q.cond.Signal()
	return nil
}

// Pop waits for and returns the oldest packet. Blocks while empty.
// Returns (0, nil, nil) when the queue is closed and drained.
func (q *PacketQueue) Pop() (RouteID, []byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.packets) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.packets) == 0 {
		return 0, nil, nil // closed and empty
	}
	pkt := q.packets[0]
	q.packets = q.packets[1:]
	return pkt.routeID, pkt.payload, nil
}

// Len returns the current queue depth (for diagnostics/metrics).
func (q *PacketQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.packets)
}

// Close wakes any waiting Pop caller and prevents further pushes.
func (q *PacketQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// ── RelayEndpoint ────────────────────────────────────────────────────────────

// RelayEndpoint implements conn.Endpoint, encoding a Relay RouteID as a
// virtual endpoint so the Bind can extract the RouteID during Send() and
// create virtual source endpoints during packet delivery.
//
// The RouteID is NOT a peer identity, WG key, or Doll identity — it is
// runtime routing topology only.
type RelayEndpoint struct {
	routeID RouteID
}

// ParseRelayEndpoint parses "relay:<integer>" into a RelayEndpoint.
// The integer is the RouteID in decimal. RouteID 0 and values above
// MaxRouteID are rejected: zero is the "no route" sentinel and values
// beyond MaxRouteID cannot be encoded in a frame.
func ParseRelayEndpoint(s string) (*RelayEndpoint, error) {
	if !strings.HasPrefix(s, RelayEndpointPrefix) {
		return nil, ErrEndpointFormat
	}
	idStr := s[len(RelayEndpointPrefix):]
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("relay: parse route ID: %w", err)
	}
	if id == 0 || RouteID(id) > MaxRouteID {
		return nil, fmt.Errorf("relay: route ID out of range: %d", id)
	}
	return &RelayEndpoint{routeID: RouteID(id)}, nil
}

// RelayEndpointString builds the string form of a relay endpoint.
func RelayEndpointString(routeID RouteID) string {
	return fmt.Sprintf("%s%d", RelayEndpointPrefix, uint64(routeID))
}

// getRouteID returns the RouteID from a RelayEndpoint.
func (e *RelayEndpoint) getRouteID() RouteID {
	return e.routeID
}

// ── conn.Endpoint interface ─────────────────────────────────────────────────

func (e *RelayEndpoint) ClearSrc() {}

func (e *RelayEndpoint) SrcToString() string {
	return "relay:source"
}

func (e *RelayEndpoint) DstToString() string {
	return RelayEndpointString(e.routeID)
}

// DstToBytes returns a deterministic 8-byte encoding of the RouteID for
// WireGuard's MAC2 cookie computation.
func (e *RelayEndpoint) DstToBytes() []byte {
	b := make([]byte, 8)
	v := uint64(e.routeID)
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
	return b
}

// DstIP returns a unique-link-local IPv6 address embedding the RouteID.
func (e *RelayEndpoint) DstIP() netip.Addr {
	id := [16]byte{}
	id[0] = 0xfd
	id[1] = 0x00
	v := uint64(e.routeID)
	id[8] = byte(v >> 56)
	id[9] = byte(v >> 48)
	id[10] = byte(v >> 40)
	id[11] = byte(v >> 32)
	id[12] = byte(v >> 24)
	id[13] = byte(v >> 16)
	id[14] = byte(v >> 8)
	id[15] = byte(v)
	return netip.AddrFrom16(id)
}

func (e *RelayEndpoint) SrcIP() netip.Addr {
	return netip.Addr{}
}

// ── RelayTransport ───────────────────────────────────────────────────────────

// RelayTransport implements conn.Bind, routing encrypted WireGuard datagrams
// through the Core ↔ Relay control tunnel.
type RelayTransport struct {
	client *ControlClient

	receiveMu  sync.Mutex
	receiveFns []conn.ReceiveFunc
	queue      *PacketQueue
	closed     bool
}

// NewRelayTransport creates a RelayTransport backed by the given control client.
func NewRelayTransport(client *ControlClient) *RelayTransport {
	return &RelayTransport{
		client: client,
		queue:  NewPacketQueue(MaxQueuePackets),
	}
}

// ── conn.Bind interface ─────────────────────────────────────────────────────

// Open puts the bind into service and returns the receive functions for the
// WG device. port is ignored (relay mode uses the control tunnel, not UDP);
// actualPort is returned as 0.
//
// Open is reopenable: the real WG runtime's BindUpdate() closes and then
// reopens the bind (e.g. on Up() or listen_port changes). A previously
// closed transport gets a fresh queue and re-registered frame handler.
func (t *RelayTransport) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	t.receiveMu.Lock()
	defer t.receiveMu.Unlock()

	if t.closed {
		// Reopen: BindUpdate closes the bind before opening it again.
		t.closed = false
		t.receiveFns = nil
		t.queue = NewPacketQueue(MaxQueuePackets)
	}
	if len(t.receiveFns) > 0 {
		return t.receiveFns, 0, nil
	}

	recvFn := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		routeID, packet, err := t.queue.Pop()
		if err != nil {
			return 0, err
		}
		if packet == nil {
			// Queue closed and drained. Per the conn.Bind contract, receive
			// funcs must return net.ErrClosed after Close() so the WG device
			// terminates its receive loop cleanly.
			return 0, net.ErrClosed
		}
		if len(packets) < 1 || len(sizes) < 1 || len(eps) < 1 {
			return 0, errors.New("relay: receive func buffers too small")
		}
		// Copy the datagram into the device's buffer. The slice header in
		// packets[0] must NOT be shortened here: the WG device reuses the
		// same bufs array across receive calls, and a permanently resliced
		// (smaller) buffer makes every later, larger datagram fail with
		// "receive buffer too small" — which, after the device's death
		// spiral, kills the receive routine permanently.
		copy(packets[0], packet)
		sizes[0] = len(packet)
		eps[0] = &RelayEndpoint{routeID: routeID}
		return 1, nil
	}
	t.receiveFns = []conn.ReceiveFunc{recvFn}

	// Register binary frame handler on the control client.
	t.client.SetFrameHandler(func(frame *Frame) error {
		return t.queue.Push(frame.RouteID, frame.Payload)
	})

	return t.receiveFns, 0, nil
}

// Close stops the transport and cleans up.
func (t *RelayTransport) Close() error {
	t.receiveMu.Lock()
	t.closed = true
	t.receiveMu.Unlock()

	t.queue.Close()
	t.client.ClearFrameHandler()
	return nil
}

func (t *RelayTransport) SetMark(mark uint32) error {
	return nil
}

// Send sends encrypted WireGuard datagrams as relay frames over the control tunnel.
func (t *RelayTransport) Send(bufs [][]byte, ep conn.Endpoint) error {
	t.receiveMu.Lock()
	defer t.receiveMu.Unlock()

	if t.closed {
		return ErrTransportClosed
	}
	if len(bufs) == 0 {
		return nil
	}

	// ep is passed as conn.Endpoint from the WG device. At runtime it is
	// always a *RelayEndpoint when this transport is active.
	rep, ok := ep.(*RelayEndpoint)
	if !ok || rep == nil {
		return ErrEndpointType
	}
	routeID := rep.getRouteID()

	for i := range bufs {
		frame := &Frame{
			Version: ProtocolVersion,
			RouteID: routeID,
			Payload: bufs[i],
		}
		if err := t.client.writeFrame(frame); err != nil {
			return fmt.Errorf("relay: send frame: %w", err)
		}
	}
	return nil
}

func (t *RelayTransport) ParseEndpoint(s string) (conn.Endpoint, error) {
	ep, err := ParseRelayEndpoint(s)
	if err != nil {
		return nil, err
	}
	return ep, nil
}

func (t *RelayTransport) BatchSize() int {
	return conn.IdealBatchSize
}
