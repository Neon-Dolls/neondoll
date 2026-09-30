package relay

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// connect dials the Relay WSS endpoint and performs the registration exchange.
// Registration is synchronous (before readLoop starts). On success it starts
// new readLoop and pingLoop goroutines for this connection.
func (c *ControlClient) connect(ctx context.Context) error {
	// Cancel any previous connection's goroutines and close the old WebSocket
	// before dialing a new one. This prevents accumulation of active sockets
	// and goroutines across reconnect cycles.
	c.connMu.Lock()
	if c.connCancel != nil {
		c.connCancel()
	}
	oldConn := c.conn
	c.conn = nil
	connCtx, connCancel := context.WithCancel(c.ctx)
	c.connCancel = connCancel
	c.connMu.Unlock()

	if oldConn != nil {
		oldConn.Close()
	}

	// Fail all pending operations from the old connection promptly.
	// This prevents stale operations from surviving a dead WebSocket.
	c.failPending(ErrConnectionLost)

	// Bump the connection generation so that any old readLoop still running
	// cannot dispatch responses to new pending operations.
	gen := c.connGen.Add(1)

	maxMsgSize := int64(MaxControlMessageSize)
	if maxMsgSize <= 0 {
		maxMsgSize = 65536
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: c.config.HandshakeTimeout,
	}

	header := http.Header{}
	header.Set("User-Agent", "NeonDoll-Core/0.1")

	conn, _, err := dialer.DialContext(ctx, c.config.RelayURL, header)
	if err != nil {
		connCancel()
		return fmt.Errorf("relay: WSS dial %q: %w", c.config.RelayURL, err)
	}

	conn.SetReadLimit(maxMsgSize)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
	})

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	// Send Register synchronously
	regData, err := MarshalControl(&Register{
		Type:  CmdRegister,
		Token: c.config.RegistrationToken,
	})
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: marshal Register: %w", err)
	}

	if err := conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		c.closeConn(conn)
		connCancel()
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, regData); err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: write Register: %w", err)
	}

	// Read response synchronously (with handshake timeout deadline)
	_ = conn.SetReadDeadline(time.Now().Add(c.config.HandshakeTimeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: read Register response: %w", err)
	}

	// Reset to longer read deadline for normal operation
	_ = conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))

	resp, err := UnmarshalControl(raw)
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: unmarshal Register response: %w", err)
	}

	switch m := resp.(type) {
	case *Registered:
		// Validate the received control message before accepting it.
		// A malformed Registered (e.g. empty RelayID) must never be
		// treated as success.
		if err := ValidateControl(m); err != nil {
			c.closeConn(conn)
			connCancel()
			return fmt.Errorf("relay: invalid Registered from Relay: %w", err)
		}
		c.relayID.Store(string(m.RelayID))
		c.connected.Store(true)

		// Start per-connection goroutines, passing the generation so
		// dispatch is scoped to this connection.
		go c.readLoop(connCtx, conn, gen)
		go c.pingLoop(connCtx, conn)

		return nil
	case *RelayError:
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("%w: code=%q message=%q", ErrRegistrationFailed, m.Code, m.Message)
	default:
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: unexpected registration response type: %T", resp)
	}
}

// closeConn is a helper to clean up a connection reference.
func (c *ControlClient) closeConn(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.connMu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.connMu.Unlock()
	conn.Close()
}

// writeMsg writes pre-marshalled JSON data on the WebSocket.
func (c *ControlClient) writeMsg(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}

	if err := conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// readLoop reads control messages from the WebSocket and dispatches
// them to the appropriate pending operation's channel.
func (c *ControlClient) readLoop(ctx context.Context, conn *websocket.Conn, gen int64) {
	defer func() {
		// Only set connected=false if this is the current connection.
		// In the window between a failed readLoop exit and the new
		// connection being established, connected stays false — the
		// reconnectLoop handles this correctly.
		c.connMu.Lock()
		isCurrent := c.conn == conn
		c.connMu.Unlock()
		if isCurrent {
			c.connected.Store(false)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout)); err != nil {
			return
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		msg, err := UnmarshalControl(raw)
		if err != nil {
			continue
		}

		// Validate every received control message before dispatching.
		// Malformed messages are silently dropped — the caller will
		// time out or observe an error via other means. This ensures
		// fail-closed behaviour even when the Relay is compromised or
		// misbehaving.
		if err := ValidateControl(msg); err != nil {
			continue
		}

		c.dispatch(msg, gen)
	}
}

// pingLoop periodically sends WebSocket-level ping frames.
func (c *ControlClient) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.connMu.Lock()
			cur := c.conn
			c.connMu.Unlock()
			if cur != conn {
				// This is an old connection; exit and let the new one's loop handle pings
				return
			}
			c.writeMu.Lock()
			_ = conn.WriteControl(websocket.PingMessage, nil,
				time.Now().Add(c.config.WriteTimeout))
			c.writeMu.Unlock()
		}
	}
}
