package relay

import (
	"context"
	"fmt"
)

// failPending fails all outstanding pending operations with the given error
// and removes them from the pending map. Uses non-blocking sends so it never
// blocks on a full channel (the exchange's caller may have already consumed
// the channel value).
func (c *ControlClient) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for k, op := range c.pending {
		if op != nil {
			select {
			case op.ch <- errorOrMsg{err: err}:
			default:
			}
		}
		delete(c.pending, k)
	}
}

// exchange sends a control message and waits for its response.
func (c *ControlClient) exchange(ctx context.Context, opKey string, msg any) (any, error) {
	data, err := MarshalControl(msg)
	if err != nil {
		return nil, fmt.Errorf("relay: marshal %s: %w", opKey, err)
	}

	ch := make(chan errorOrMsg, 1)

	gen := c.connGen.Load()

	c.pendingMu.Lock()
	// If a stale pending op exists under the same key (from a prior
	// connection generation that wasn't cleaned up), fail it preemptively
	// to prevent leaking a goroutine blocked on a channel that will
	// never be consumed.
	if existing := c.pending[opKey]; existing != nil {
		select {
		case existing.ch <- errorOrMsg{err: ErrConnectionLost}:
		default:
		}
	}
	c.pending[opKey] = &pendingOp{ch: ch, connGen: gen}
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, opKey)
		c.pendingMu.Unlock()
	}()

	if err := c.writeMsg(data); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, ErrClientClosed
	case eom := <-ch:
		return eom.msg, eom.err
	}
}

// dispatch routes a control response to its pending operation, but only
// if the pending operation belongs to the same connection generation as
// the readLoop that received it. This prevents stale readLoop goroutines
// from colliding with operations from a newer connection.
func (c *ControlClient) dispatch(msg any, gen int64) {
	switch m := msg.(type) {
	case *Registered:
		c.pendingMu.Lock()
		op := c.pending["register"]
		c.pendingMu.Unlock()
		if op != nil && op.connGen == gen {
			select {
			case op.ch <- errorOrMsg{msg: m}:
			default:
			}
		}

	case *RouteOpened:
		c.dispatchByRoute(m.RouteID, msg, nil, gen)

	case *RouteClosed:
		c.dispatchByRoute(m.RouteID, msg, nil, gen)

	case *RelayError:
		if m.RouteID != 0 {
			c.dispatchByRoute(m.RouteID, msg, nil, gen)
		} else {
			// Register-scoped error; dispatch to "register" pending key.
			c.pendingMu.Lock()
			op := c.pending["register"]
			c.pendingMu.Unlock()
			if op != nil && op.connGen == gen {
				select {
				case op.ch <- errorOrMsg{msg: m}:
				default:
				}
			}
		}

	default:
	}
}

func (c *ControlClient) dispatchByRoute(routeID RouteID, msg any, msgErr error, gen int64) {
	key := routeOpKey(routeID)
	c.pendingMu.Lock()
	op := c.pending[key]
	c.pendingMu.Unlock()
	if op != nil && op.connGen == gen {
		select {
		case op.ch <- errorOrMsg{msg: msg, err: msgErr}:
		default:
		}
	}
}

func routeOpKey(routeID RouteID) string {
	return fmt.Sprintf("route:%d", routeID)
}
