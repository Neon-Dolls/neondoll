package relay

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RegistrationID identifies a Core's registration with the Relay.
// It is opaque routing information, not a Doll identity or authority.
type RegistrationID string

// RouteState tracks the lifecycle phase of a relay route.
type RouteState int

const (
	// RouteStateAllocated means the route was created but not yet opened.
	RouteStateAllocated RouteState = iota
	// RouteStateOpening means the route is in the process of being opened.
	RouteStateOpening
	// RouteStateOpen means the route is active and ready for traffic.
	RouteStateOpen
	// RouteStateClosing means the route is in the process of being closed.
	RouteStateClosing
	// RouteStateClosed means the route has been closed and may be reclaimed.
	RouteStateClosed
)

var routeStateNames = map[RouteState]string{
	RouteStateAllocated: "allocated",
	RouteStateOpening:   "opening",
	RouteStateOpen:      "open",
	RouteStateClosing:   "closing",
	RouteStateClosed:    "closed",
}

func (s RouteState) String() string {
	if name, ok := routeStateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("RouteState(%d)", int(s))
}

// --- Errors ---

var (
	ErrRegistrationNotFound    = errors.New("relay: registration not found")
	ErrRegistrationExists      = errors.New("relay: registration already exists")
	ErrRegistrationLimit       = errors.New("relay: max registrations reached")
	ErrRouteNotFound           = errors.New("relay: route not found")
	ErrTooManyRoutes           = errors.New("relay: max routes reached")
	ErrRouteAlreadyExists      = errors.New("relay: route already exists")
	ErrRouteWrongOwner         = errors.New("relay: route belongs to a different registration")
	ErrRouteStateTransition    = errors.New("relay: invalid route state transition")
	ErrRouteCredentialsInvalid = errors.New("relay: route credentials are invalid")
)

// --- Registry entries ---

// RouteEntry holds the state of a single relay route.
type RouteEntry struct {
	RouteID        RouteID
	State          RouteState
	RegistrationID RegistrationID
	Credentials    RouteCredentials
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Endpoint       string // set by Relay when route is opened (M4.3+)
}

// RegistrationEntry holds the state of a Core registration.
type RegistrationEntry struct {
	ID            RegistrationID
	TokenHash     string
	CreatedAt     time.Time
	LastKeepalive time.Time
	routes        map[RouteID]struct{}
}

// RegistrationEntry provides read-only access to route IDs.
func (e *RegistrationEntry) RouteIDs() []RouteID {
	ids := make([]RouteID, 0, len(e.routes))
	for id := range e.routes {
		ids = append(ids, id)
	}
	return ids
}

// Registry manages registrations and their routes with ownership/isolation.
//
// Each Core registration owns its routes. A route belongs to exactly one
// registration; cross-registration access is forbidden. The registry enforces
// bounded state via configured limits.
type Registry struct {
	mu               sync.RWMutex
	registrations    map[RegistrationID]*RegistrationEntry
	routes           map[RouteID]*RouteEntry
	maxRegistrations int
	maxRoutes        int
}

// NewRegistry creates an empty Registry with the given limits.
func NewRegistry(maxRegistrations, maxRoutes int) *Registry {
	return &Registry{
		registrations:    make(map[RegistrationID]*RegistrationEntry),
		routes:           make(map[RouteID]*RouteEntry),
		maxRegistrations: maxRegistrations,
		maxRoutes:        maxRoutes,
	}
}

// --- Registration management ---

// AddRegistration creates a new registration with the given ID and token hash.
// Returns ErrRegistrationExists if the ID is already registered,
// ErrRegistrationLimit if the limit would be exceeded.
func (r *Registry) AddRegistration(id RegistrationID, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.registrations[id]; ok {
		return ErrRegistrationExists
	}
	if len(r.registrations) >= r.maxRegistrations {
		return ErrRegistrationLimit
	}

	now := time.Now()
	r.registrations[id] = &RegistrationEntry{
		ID:            id,
		TokenHash:     tokenHash,
		CreatedAt:     now,
		LastKeepalive: now,
		routes:        make(map[RouteID]struct{}),
	}
	_ = now // used via RegistrationEntry
	return nil
}

// RemoveRegistration removes a registration and all its routes.
func (r *Registry) RemoveRegistration(id RegistrationID) []RouteID {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.registrations[id]
	if !ok {
		return nil
	}

	routeIDs := reg.RouteIDs()
	for _, rid := range routeIDs {
		delete(r.routes, rid)
	}
	delete(r.registrations, id)
	return routeIDs
}

// Registration returns a registration entry (read-only snapshot).
func (r *Registry) Registration(id RegistrationID) (*RegistrationEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	reg, ok := r.registrations[id]
	if !ok {
		return nil, false
	}
	// return a copy
	cp := *reg
	cp.routes = make(map[RouteID]struct{}, len(reg.routes))
	for rid := range reg.routes {
		cp.routes[rid] = struct{}{}
	}
	return &cp, true
}

// Keepalive updates the last-keepalive timestamp for a registration.
func (r *Registry) Keepalive(id RegistrationID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.registrations[id]
	if !ok {
		return ErrRegistrationNotFound
	}
	reg.LastKeepalive = time.Now()
	return nil
}

// --- Route management ---

// GenerateRouteCredentials creates cryptographically random route-scoped
// credentials. The token is a hex-encoded 32-byte value (64 hex chars).
// It is NOT derived from RouteID, registration token, WireGuard identity,
// Doll identity, or any other persistent secret — every call produces
// independent random credentials.
func GenerateRouteCredentials() (RouteCredentials, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return RouteCredentials{}, fmt.Errorf("relay: generate route credentials: %w", err)
	}
	return RouteCredentials{Token: hex.EncodeToString(b)}, nil
}

// MustGenerateRouteCredentials is like GenerateRouteCredentials but panics
// on failure. Suitable for tests and startup where failure is fatal.
func MustGenerateRouteCredentials() RouteCredentials {
	c, err := GenerateRouteCredentials()
	if err != nil {
		panic(err)
	}
	return c
}

// AllocateRoute creates a new route owned by the given registration and
// assigns it independent random credentials. The route starts in
// RouteStateAllocated.
//
// Returns ErrRegistrationNotFound, ErrRouteAlreadyExists, or ErrRouteLimit.
func (r *Registry) AllocateRoute(regID RegistrationID, routeID RouteID) (*RouteEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.registrations[regID]
	if !ok {
		return nil, ErrRegistrationNotFound
	}
	if _, exists := r.routes[routeID]; exists {
		return nil, ErrRouteAlreadyExists
	}
	if len(r.routes) >= r.maxRoutes {
		return nil, ErrTooManyRoutes
	}

	creds, err := GenerateRouteCredentials()
	if err != nil {
		return nil, ErrRouteCredentialsInvalid
	}

	now := time.Now()
	entry := &RouteEntry{
		RouteID:        routeID,
		State:          RouteStateAllocated,
		RegistrationID: regID,
		Credentials:    creds,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	r.routes[routeID] = entry
	reg.routes[routeID] = struct{}{}
	return entry, nil
}

// OpenRoute transitions a route from Allocated to Open (via Opening).
// The caller must verify credentials match; the registry only tracks state.
func (r *Registry) OpenRoute(regID RegistrationID, routeID RouteID) (*RouteEntry, error) {
	entry, err := r.getOwnedRoute(regID, routeID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-fetch under write lock
	entry = r.routes[routeID]
	if entry == nil || entry.RegistrationID != regID {
		return nil, ErrRouteNotFound
	}

	switch entry.State {
	case RouteStateAllocated:
		entry.State = RouteStateOpening
		entry.State = RouteStateOpen
		entry.UpdatedAt = time.Now()
		cp := *entry
		return &cp, nil
	default:
		return nil, ErrRouteStateTransition
	}
}

// CloseRoute transitions a route to Closed (via Closing) and removes it.
func (r *Registry) CloseRoute(regID RegistrationID, routeID RouteID) (*RouteEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.routes[routeID]
	if !ok {
		return nil, ErrRouteNotFound
	}
	if entry.RegistrationID != regID {
		return nil, ErrRouteWrongOwner
	}
	if entry.State == RouteStateClosed {
		return nil, ErrRouteStateTransition
	}

	entry.State = RouteStateClosing
	entry.State = RouteStateClosed
	entry.UpdatedAt = time.Now()

	// Remove from registry
	cp := *entry
	delete(r.routes, routeID)
	if reg, ok := r.registrations[regID]; ok {
		delete(reg.routes, routeID)
	}
	return &cp, nil
}

// Route returns a route entry (read-only copy).
func (r *Registry) Route(routeID RouteID) (*RouteEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.routes[routeID]
	if !ok {
		return nil, false
	}
	cp := *entry
	return &cp, true
}

// --- Queries ---

// RouteCount returns the number of active (non-closed) routes.
func (r *Registry) RouteCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// RegistrationCount returns the number of active registrations.
func (r *Registry) RegistrationCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.registrations)
}

// --- Liveness ---

// ExpireStaleResult captures the outcome of a stale-expiration sweep.
type ExpireStaleResult struct {
	// StaleRoutes lists routes that exceeded RouteTimeout while their
	// owning registration remained alive.
	StaleRoutes []RouteID
	// ExpiredRegistrations lists registrations that exceeded
	// RegistrationTimeout.
	ExpiredRegistrations []RegistrationID
	// RoutesExpiredViaReg lists routes removed because their owning
	// registration expired.
	RoutesExpiredViaReg []RouteID
}

// ExpireStale removes stale routes and registrations whose last keepalive
// exceeds the given timeout. It returns a structured result distinguishing
// independently expired routes from routes removed due to registration expiry.
func (r *Registry) ExpireStale(regTimeout, routeTimeout time.Duration) ExpireStaleResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var result ExpireStaleResult

	for id, reg := range r.registrations {
		// Phase 1: independently stale routes (route-level timeout)
		var staleRoutes []RouteID
		for rid := range reg.routes {
			entry, ok := r.routes[rid]
			if !ok {
				continue
			}
			if now.Sub(entry.UpdatedAt) > routeTimeout {
				staleRoutes = append(staleRoutes, rid)
			}
		}
		for _, rid := range staleRoutes {
			delete(r.routes, rid)
			delete(reg.routes, rid)
		}
		result.StaleRoutes = append(result.StaleRoutes, staleRoutes...)

		// Phase 2: registration-level staleness
		if now.Sub(reg.LastKeepalive) > regTimeout {
			// Collect remaining routes (those not already removed as stale)
			remaining := reg.RouteIDs()
			for _, rid := range remaining {
				delete(r.routes, rid)
			}
			result.ExpiredRegistrations = append(result.ExpiredRegistrations, id)
			result.RoutesExpiredViaReg = append(result.RoutesExpiredViaReg, remaining...)
			delete(r.registrations, id)
		}
	}

	return result
}

// --- internal helpers ---

// getOwnedRoute returns a read-only copy of a route after verifying
// ownership under a read lock.
func (r *Registry) getOwnedRoute(regID RegistrationID, routeID RouteID) (*RouteEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.routes[routeID]
	if !ok {
		return nil, ErrRouteNotFound
	}
	if entry.RegistrationID != regID {
		return nil, ErrRouteWrongOwner
	}
	cp := *entry
	return &cp, nil
}

// RouteCredentialsFromEntry returns the credentials for a route.
// Used by the service layer to validate control message credentials.
func (r *Registry) RouteCredentialsFromEntry(routeID RouteID) (RouteCredentials, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.routes[routeID]
	if !ok {
		return RouteCredentials{}, ErrRouteNotFound
	}
	return entry.Credentials, nil
}

// GetRoute returns a route entry (internal — no copy, caller must hold lock).
// Only safe under r.mu read lock.
func (r *Registry) getRoute(routeID RouteID) (*RouteEntry, bool) {
	entry, ok := r.routes[routeID]
	if !ok {
		return nil, false
	}
	return entry, true
}
