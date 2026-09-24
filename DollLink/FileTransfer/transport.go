package filetransfer

import (
	"encoding/json"
	"fmt"

	"github.com/gorilla/websocket"
)

// ── WebSocket Dual-Frame Connection ─────────────────────────────────────

// MessageType constants for gorilla/websocket.
const (
	wsText   = websocket.TextMessage
	wsBinary = websocket.BinaryMessage
)

// WSConn wraps a gorilla/websocket.Conn to provide dual-frame access:
// text frames for JSON control messages, binary frames for NDF1 data.
//
// Each Read call blocks until a message of the expected type arrives.
// Unexpected message types are returned as errors, preserving the order
// of delivery.
type WSConn struct {
	conn *websocket.Conn
}

// NewWSConn wraps an existing WebSocket connection for dual-frame file
// transfer operations.
func NewWSConn(conn *websocket.Conn) *WSConn {
	return &WSConn{conn: conn}
}

// Underlying returns the raw gorilla/websocket connection.
func (wsc *WSConn) Underlying() *websocket.Conn {
	return wsc.conn
}

// ── Writing ─────────────────────────────────────────────────────────────

// WriteControl encodes msg as JSON and sends it as a WebSocket text frame.
func (wsc *WSConn) WriteControl(msg *ControlMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("filetransfer: marshal control message: %w", err)
	}
	return wsc.conn.WriteMessage(wsText, data)
}

// WriteFrame encodes the NDF1 frame and sends it as a WebSocket binary
// frame. The frame is validated before sending.
func (wsc *WSConn) WriteFrame(frame *NDF1Frame) error {
	data, err := frame.MarshalBinary()
	if err != nil {
		return fmt.Errorf("filetransfer: marshal NDF1 frame: %w", err)
	}
	return wsc.conn.WriteMessage(wsBinary, data)
}

// ── Reading ─────────────────────────────────────────────────────────────

// ReadControl blocks until the next text/JSON message arrives and decodes
// it as a ControlMessage. Binary messages are skipped with an error — the
// caller should drain them before reading control, or use ReadAny.
func (wsc *WSConn) ReadControl() (*ControlMessage, error) {
	for {
		msgType, data, err := wsc.conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("filetransfer: read control: %w", err)
		}
		switch msgType {
		case wsText:
			var msg ControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				return nil, fmt.Errorf("filetransfer: decode control message: %w", err)
			}
			return &msg, nil
		case wsBinary:
			// A binary frame arrived when control was expected.
			// The caller is responsible for reading in the right order.
			return nil, fmt.Errorf("filetransfer: expected control message, got binary NDF1 frame")
		default:
			return nil, fmt.Errorf("filetransfer: unknown WebSocket message type %d", msgType)
		}
	}
}

// ReadFrame blocks until the next binary message arrives and decodes it
// as an NDF1Frame. Text messages are skipped with an error.
func (wsc *WSConn) ReadFrame() (*NDF1Frame, error) {
	for {
		msgType, data, err := wsc.conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("filetransfer: read frame: %w", err)
		}
		switch msgType {
		case wsBinary:
			var frame NDF1Frame
			if err := frame.UnmarshalBinary(data); err != nil {
				return nil, fmt.Errorf("filetransfer: decode NDF1 frame: %w", err)
			}
			return &frame, nil
		case wsText:
			return nil, fmt.Errorf("filetransfer: expected NDF1 frame, got text control message")
		default:
			return nil, fmt.Errorf("filetransfer: unknown WebSocket message type %d", msgType)
		}
	}
}

// ReadAny reads the next message and returns whichever type arrives.
// It is useful in dispatcher loops that need to route by message kind.
func (wsc *WSConn) ReadAny() (any, error) {
	msgType, data, err := wsc.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("filetransfer: read any: %w", err)
	}
	switch msgType {
	case wsText:
		var msg ControlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("filetransfer: decode control message: %w", err)
		}
		return &msg, nil
	case wsBinary:
		var frame NDF1Frame
		if err := frame.UnmarshalBinary(data); err != nil {
			return nil, fmt.Errorf("filetransfer: decode NDF1 frame: %w", err)
		}
		return &frame, nil
	default:
		return nil, fmt.Errorf("filetransfer: unknown WebSocket message type %d", msgType)
	}
}

// ── Close ───────────────────────────────────────────────────────────────

// Close sends a WebSocket close frame and closes the underlying connection.
func (wsc *WSConn) Close() error {
	return wsc.conn.Close()
}

// ── Session Manager ─────────────────────────────────────────────────────

// TransferSession manages the lifecycle of one file transfer attempt over
// a dual-frame WebSocket connection.
type TransferSession struct {
	ID TransferID
	// conn is the dual-frame connection.
	conn *WSConn
	// state tracks the transfer state machine.
	state TransferState
}

// NewTransferSession creates a new session around an existing dual-frame
// connection.
func NewTransferSession(conn *WSConn, tid TransferID) *TransferSession {
	return &TransferSession{
		ID:    tid,
		conn:  conn,
		state: StateNone,
	}
}

// Conn returns the underlying dual-frame connection.
func (s *TransferSession) Conn() *WSConn {
	return s.conn
}

// State returns the current transfer state.
func (s *TransferSession) State() TransferState {
	return s.state
}

// SetState sets the current transfer state. It does not validate transitions;
// callers should use stateMachineCheckTransition or their own validation.
func (s *TransferSession) SetState(st TransferState) {
	s.state = st
}

// SendControl sends a control message on this session's connection.
func (s *TransferSession) SendControl(msg *ControlMessage) error {
	return s.conn.WriteControl(msg)
}

// SendFrame sends an NDF1 data frame on this session's connection.
func (s *TransferSession) SendFrame(frame *NDF1Frame) error {
	return s.conn.WriteFrame(frame)
}

// Close closes the underlying WebSocket connection.
func (s *TransferSession) Close() error {
	return s.conn.Close()
}
