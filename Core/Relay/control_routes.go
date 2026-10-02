package relay

import (
	"context"
	"errors"
	"fmt"
)

// OpenRoute requests a new relay route from the Relay.
func (c *ControlClient) OpenRoute(ctx context.Context, routeID RouteID, creds RouteCredentials) (*RouteOpened, error) {
	if routeID == 0 {
		return nil, errors.New("relay: routeID must be nonzero")
	}
	if creds.Token == "" {
		return nil, errors.New("relay: RouteCredentials.Token is required")
	}
	if !c.connected.Load() {
		return nil, ErrNotConnected
	}

	opKey := routeOpKey(routeID)
	resp, err := c.exchange(ctx, opKey, &RouteOpen{
		Type:        CmdRouteOpen,
		RouteID:     routeID,
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("relay: open route %d: %w", routeID, err)
	}

	switch m := resp.(type) {
	case *RouteOpened:
		// Validate the received control message before accepting it.
		// A malformed RouteOpened (e.g. missing AllocatedEndpoint)
		// must never be treated as success.
		if err := ValidateControl(m); err != nil {
			return nil, fmt.Errorf("relay: invalid RouteOpened: %w", err)
		}
		c.routesMu.Lock()
		c.routes[routeID] = &managedRoute{
			Credentials:       m.Credentials,
			AllocatedEndpoint: m.AllocatedEndpoint,
		}
		c.routesMu.Unlock()
		return m, nil
	case *RelayError:
		return nil, fmt.Errorf("relay: route %d rejected: code=%q message=%q",
			routeID, m.Code, m.Message)
	default:
		return nil, fmt.Errorf("relay: unexpected route open response type: %T", resp)
	}
}

// CloseRoute requests closing a route on the Relay.
func (c *ControlClient) CloseRoute(ctx context.Context, routeID RouteID) error {
	if routeID == 0 {
		return errors.New("relay: routeID must be nonzero")
	}
	if !c.connected.Load() {
		return ErrNotConnected
	}

	opKey := routeOpKey(routeID)
	resp, err := c.exchange(ctx, opKey, &RouteClose{
		Type:    CmdRouteClose,
		RouteID: routeID,
	})
	if err != nil {
		return fmt.Errorf("relay: close route %d: %w", routeID, err)
	}

	switch m := resp.(type) {
	case *RouteClosed:
		// Validate the received control message before accepting it.
		// A malformed RouteClosed (e.g. missing RouteID) must never
		// be treated as success.
		if err := ValidateControl(m); err != nil {
			return fmt.Errorf("relay: invalid RouteClosed: %w", err)
		}
		c.routesMu.Lock()
		delete(c.routes, routeID)
		c.routesMu.Unlock()
		return nil
	case *RelayError:
		return fmt.Errorf("relay: close route %d rejected: code=%q message=%q",
			routeID, m.Code, m.Message)
	default:
		return fmt.Errorf("relay: unexpected route close response type: %T", resp)
	}
}
