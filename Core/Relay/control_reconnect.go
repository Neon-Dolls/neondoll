package relay

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// reconnectLoop watches for connection loss and reconnects with
// exponential backoff. After reconnection it re-registers and
// restores all active routes.
func (c *ControlClient) reconnectLoop() {
	defer close(c.done)

	poll := 100 * time.Millisecond
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// Wait until the connection drops, checking for close signal each cycle
		for c.connected.Load() {
			if c.closed.Load() {
				return
			}
			timer.Reset(poll)
			select {
			case <-c.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}

		if c.closed.Load() {
			return
		}

		backoff := c.config.ReconnectInitial

		for {
			select {
			case <-c.ctx.Done():
				return
			default:
			}

			jitter := time.Duration(0)
			if c.config.ReconnectJitter > 0 {
				n, err := rand.Int(rand.Reader, big.NewInt(int64(c.config.ReconnectJitter)))
				if err == nil {
					jitter = time.Duration(n.Int64())
				}
			}

			timer.Reset(backoff + jitter)
			select {
			case <-c.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}

			if err := c.connect(c.ctx); err != nil {
				c.LastConnectError.Store(err.Error())
				next := time.Duration(float64(backoff) * c.config.ReconnectMultiplier)
				if next > c.config.ReconnectMax {
					next = c.config.ReconnectMax
				}
				backoff = next
				continue
			}

			c.LastConnectError.Store("")
			c.Reconnects.Add(1)

			if err := c.restoreRoutes(context.Background()); err != nil {
				_ = err
			}
			break
		}
	}
}

// restoreRoutes re-opens all tracked routes on the new connection.
func (c *ControlClient) restoreRoutes(ctx context.Context) error {
	c.routesMu.RLock()
	type routeEntry struct {
		ID RouteID
		Mr *managedRoute
	}
	routes := make([]routeEntry, 0, len(c.routes))
	for id, mr := range c.routes {
		routes = append(routes, routeEntry{id, mr})
	}
	c.routesMu.RUnlock()

	if len(routes) == 0 {
		return nil
	}

	var firstErr error
	for _, r := range routes {
		resp, err := c.exchange(ctx, routeOpKey(r.ID), &RouteOpen{
			Type:        CmdRouteOpen,
			RouteID:     r.ID,
			Credentials: r.Mr.Credentials,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		switch m := resp.(type) {
		case *RouteOpened:
			if err := ValidateControl(m); err != nil {
				// Invalid RouteOpened from Relay during restore;
				// treat as a route loss, not a fatal error.
				if firstErr == nil {
					firstErr = fmt.Errorf("relay: restore route %d: invalid RouteOpened: %w", r.ID, err)
				}
				break
			}
			c.routesMu.Lock()
			if existing, ok := c.routes[r.ID]; ok {
				existing.AllocatedEndpoint = m.AllocatedEndpoint
			}
			c.routesMu.Unlock()
			c.RoutesRestored.Add(1)
		case *RelayError:
			c.routesMu.Lock()
			delete(c.routes, r.ID)
			c.routesMu.Unlock()
			if firstErr == nil {
				firstErr = fmt.Errorf("relay: restore route %d rejected: code=%q", r.ID, m.Code)
			}
		}
	}

	return firstErr
}
