package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

// ExecutionRequestHandler handles a Core-to-Body execution request. It is
// installed only after the caller has established trust in the Core
// connection; configuring a handler does not authenticate that connection.
type ExecutionRequestHandler interface {
	HandleExecutionRequest(context.Context, execution.Request) (execution.Result, error)
	UnavailableExecutionResult(execution.Request) (execution.Result, error)
}

// EventReceiver observes non-execution Doll Link events delivered to a Body.
type EventReceiver interface {
	HandleEvent(context.Context, *events.Event) error
}

// EventReceiverFunc adapts a function to EventReceiver.
type EventReceiverFunc func(context.Context, *events.Event) error

func (f EventReceiverFunc) HandleEvent(ctx context.Context, event *events.Event) error {
	return f(ctx, event)
}

// ClientConfig configures a Body-side Doll Link WebSocket connection.
type ClientConfig struct {
	// AuthenticatedExecutionHandler is enabled only when the caller has
	// independently authenticated the connected Core. Nil fails closed:
	// execution requests are never dispatched.
	AuthenticatedExecutionHandler ExecutionRequestHandler
	EventReceiver                 EventReceiver

	// MaxConcurrentExecutions bounds in-flight Body capability invocations.
	// Additional correlatable requests receive an immediate unavailable result.
	// Values less than one use DefaultMaxConcurrentExecutions.
	MaxConcurrentExecutions int
}

// DefaultMaxConcurrentExecutions bounds Body-side capability work when the
// caller does not provide a limit.
const DefaultMaxConcurrentExecutions = 16

// Client owns the single read loop and all serialized writes for one
// Body-side Doll Link WebSocket connection.
type Client struct {
	conn      *websocket.Conn
	log       Logger
	execution ExecutionRequestHandler
	events    EventReceiver
	slots     chan struct{}

	writeMu sync.Mutex
	runMu   sync.Mutex
	running bool
	close   sync.Once
	done    chan struct{}
	workers sync.WaitGroup
}

// NewClient takes ownership of an established WebSocket connection.
func NewClient(conn *websocket.Conn, cfg ClientConfig, log Logger) (*Client, error) {
	if conn == nil {
		return nil, fmt.Errorf("ws: Body client connection is required")
	}
	maxConcurrent := cfg.MaxConcurrentExecutions
	if maxConcurrent < 1 {
		maxConcurrent = DefaultMaxConcurrentExecutions
	}
	return &Client{
		conn:      conn,
		log:       log,
		execution: cfg.AuthenticatedExecutionHandler,
		events:    cfg.EventReceiver,
		slots:     make(chan struct{}, maxConcurrent),
		done:      make(chan struct{}),
	}, nil
}

// Run owns the connection read loop until the connection or context closes.
// Execution handlers run concurrently so one capability cannot block receipt
// of another request or an Event/Action message.
func (c *Client) Run(ctx context.Context) error {
	c.runMu.Lock()
	if c.running {
		c.runMu.Unlock()
		return fmt.Errorf("ws: Body client read loop already running")
	}
	c.running = true
	c.runMu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			_ = c.conn.Close()
		case <-watchDone:
		}
	}()

	err := c.readLoop(runCtx)
	cancel()
	close(watchDone)
	_ = c.conn.Close()
	c.workers.Wait()
	c.close.Do(func() { close(c.done) })
	return err
}

// Close terminates the connection and unblocks Run.
func (c *Client) Close() error { return c.conn.Close() }

// Done closes after Run and all in-flight execution handlers exit.
func (c *Client) Done() <-chan struct{} { return c.done }

// SendEvent sends a Body-originated event through the same serialized writer
// used for execution results.
func (c *Client) SendEvent(ctx context.Context, event events.Event) error {
	return c.write(ctx, event)
}

func (c *Client) readLoop(ctx context.Context) error {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil {
			c.warn("ws Body client invalid message", err)
			continue
		}
		switch envelope.Type {
		case string(execution.TypeRequest):
			if c.execution == nil {
				c.warn("ws Body client rejected execution request without authenticated handler", nil)
				continue
			}
			var request execution.Request
			if err := json.Unmarshal(message, &request); err != nil {
				c.warn("ws Body client invalid execution request", err)
				continue
			}
			select {
			case c.slots <- struct{}{}:
				c.workers.Add(1)
				go c.handleExecution(ctx, request)
			default:
				result, err := c.execution.UnavailableExecutionResult(request)
				if err != nil {
					c.warn("ws Body client could not correlate saturated execution request", err)
					continue
				}
				if err := c.write(ctx, result); err != nil {
					c.warn("ws Body client unavailable result write failed", err)
				}
			}
		case string(execution.TypeResult):
			c.warn("ws Body client rejected execution result from Core connection", nil)
		default:
			if c.events == nil {
				continue
			}
			var event events.Event
			if err := json.Unmarshal(message, &event); err != nil {
				c.warn("ws Body client invalid event", err)
				continue
			}
			if err := c.events.HandleEvent(ctx, &event); err != nil {
				c.warn("ws Body client event handler failed", err)
			}
		}
	}
}

func (c *Client) handleExecution(ctx context.Context, request execution.Request) {
	defer c.workers.Done()
	defer func() { <-c.slots }()
	result, err := c.execution.HandleExecutionRequest(ctx, request)
	if err != nil {
		c.warn("ws Body client execution request rejected", err)
		return
	}
	if err := c.write(ctx, result); err != nil {
		c.warn("ws Body client execution result write failed", err)
	}
}

func (c *Client) write(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.conn.WriteJSON(value)
}

func (c *Client) warn(message string, err error) {
	if c.log == nil {
		return
	}
	if err == nil {
		c.log.Warn(message)
		return
	}
	c.log.Warn(message, map[string]any{"error": err.Error()})
}
