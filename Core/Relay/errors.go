package relay

import "errors"

var (
	ErrNotConnected  = errors.New("relay: not connected")
	ErrAlreadyClosed = errors.New("relay: already closed")
	ErrRouteInUse    = errors.New("relay: route ID already in use")
	ErrRouteNotFound = errors.New("relay: route not found")
)
