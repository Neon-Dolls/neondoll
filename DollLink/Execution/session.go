package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrDisconnected is returned to pending requests when their Doll Link
	// connection terminates.
	ErrDisconnected = errors.New("execution: Doll Link session disconnected")
	// ErrClosed indicates that an execution session is no longer usable.
	ErrClosed = errors.New("execution: session closed")
)

// RequestSender is the transport boundary used by Client. Implementations must
// serialize writes with every other writer that owns the same connection.
type RequestSender interface {
	SendExecutionRequest(context.Context, Request) error
}

// Session is an execution channel whose Body identity was authenticated by a
// trusted caller outside this package. A BodyID selects the bound session; it
// is not authentication evidence.
type Session interface {
	BodyID() string
	Execute(context.Context, string, RequestPayload) (Result, error)
	HandleResult(Result) error
	Close(error)
}

type pendingRequest struct {
	request Request
	result  chan resultOrError
}

type resultOrError struct {
	result Result
	err    error
}

// Client correlates execution requests and results for one authenticated Body.
// NewClient must only be called after a trusted connection owner has
// authenticated bodyID; the constructor does not authenticate it.
type Client struct {
	mu      sync.Mutex
	bodyID  string
	sender  RequestSender
	pending map[string]*pendingRequest
	closed  error
}

// NewClient binds execution traffic to an identity supplied by a trusted
// caller. It deliberately performs no identity discovery or authentication.
func NewClient(bodyID string, sender RequestSender) (*Client, error) {
	if bodyID == "" {
		return nil, fmt.Errorf("execution: authenticated Body identity is required")
	}
	if sender == nil {
		return nil, fmt.Errorf("execution: request sender is required")
	}
	return &Client{
		bodyID:  bodyID,
		sender:  sender,
		pending: make(map[string]*pendingRequest),
	}, nil
}

// BodyID returns the authenticated identity bound to this session.
func (c *Client) BodyID() string { return c.bodyID }

// Execute sends one canonical execution.request and waits for its correlated
// execution.result. Cancelling the context only cancels the local wait because
// Body Protocol v1 defines no execution cancellation message.
func (c *Client) Execute(ctx context.Context, dollID string, payload RequestPayload) (Result, error) {
	request := Request{
		Type:      TypeRequest,
		ID:        uuid.NewString(),
		Timestamp: time.Now().UTC(),
		DollID:    dollID,
		BodyID:    c.bodyID,
		Payload:   payload,
	}
	if err := request.Validate(SenderCore); err != nil {
		return Result{}, err
	}

	pending := &pendingRequest{request: request, result: make(chan resultOrError, 1)}
	c.mu.Lock()
	if c.closed != nil {
		err := c.closed
		c.mu.Unlock()
		return Result{}, err
	}
	c.pending[request.ID] = pending
	c.mu.Unlock()

	if err := c.sender.SendExecutionRequest(ctx, request); err != nil {
		c.remove(request.ID, pending)
		return Result{}, err
	}

	select {
	case outcome := <-pending.result:
		return outcome.result, outcome.err
	case <-ctx.Done():
		if c.remove(request.ID, pending) {
			return Result{}, ctx.Err()
		}
		// A result won the race with cancellation and is already buffered.
		outcome := <-pending.result
		return outcome.result, outcome.err
	}
}

// HandleResult validates and dispatches a canonical execution.result. Invalid,
// mismatched, and unsolicited results never consume a pending request.
func (c *Client) HandleResult(result Result) error {
	if err := result.Validate(SenderBody); err != nil {
		return err
	}
	if result.BodyID != c.bodyID {
		return fmt.Errorf("execution: result body_id %q does not match authenticated Body %q", result.BodyID, c.bodyID)
	}

	c.mu.Lock()
	pending := c.pending[result.CorrelationID]
	if pending == nil {
		c.mu.Unlock()
		return fmt.Errorf("execution: unsolicited result for correlation_id %q", result.CorrelationID)
	}
	if err := result.ValidateResponseTo(pending.request); err != nil {
		c.mu.Unlock()
		return err
	}
	delete(c.pending, result.CorrelationID)
	c.mu.Unlock()

	pending.result <- resultOrError{result: result}
	return nil
}

// Close terminates the session and releases every pending waiter. It is
// idempotent; the first close reason is retained.
func (c *Client) Close(reason error) {
	if reason == nil {
		reason = ErrClosed
	}
	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return
	}
	c.closed = reason
	pending := c.pending
	c.pending = make(map[string]*pendingRequest)
	c.mu.Unlock()

	for _, request := range pending {
		request.result <- resultOrError{err: reason}
	}
}

func (c *Client) remove(id string, expected *pendingRequest) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[id] != expected {
		return false
	}
	delete(c.pending, id)
	return true
}
