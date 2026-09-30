package relay

import (
	"sync/atomic"
)

// Metrics holds diagnostic counters for the relay service.
// All fields are atomically updated and safe for concurrent read.
type Metrics struct {
	routesCreated                atomic.Int64
	routesClosed                 atomic.Int64
	routesFailed                 atomic.Int64
	registrationsAdded           atomic.Int64
	registrationsDropped         atomic.Int64
	routesExpiredViaRegistration atomic.Int64
	controlMessages              atomic.Int64
	controlErrors                atomic.Int64
	livenessChecks               atomic.Int64
	staleRoutesExpired           atomic.Int64
}

// NewMetrics returns an initialised Metrics instance.
func NewMetrics() *Metrics { return &Metrics{} }

// --- accessors ---

func (m *Metrics) RoutesCreated() int64                { return m.routesCreated.Load() }
func (m *Metrics) RoutesClosed() int64                 { return m.routesClosed.Load() }
func (m *Metrics) RoutesFailed() int64                 { return m.routesFailed.Load() }
func (m *Metrics) RegistrationsAdded() int64           { return m.registrationsAdded.Load() }
func (m *Metrics) RegistrationsDropped() int64         { return m.registrationsDropped.Load() }
func (m *Metrics) ControlMessages() int64              { return m.controlMessages.Load() }
func (m *Metrics) ControlErrors() int64                { return m.controlErrors.Load() }
func (m *Metrics) LivenessChecks() int64               { return m.livenessChecks.Load() }
func (m *Metrics) StaleRoutesExpired() int64           { return m.staleRoutesExpired.Load() }
func (m *Metrics) RoutesExpiredViaRegistration() int64 { return m.routesExpiredViaRegistration.Load() }

// --- incrementers (package-internal) ---

func (m *Metrics) incRoutesCreated()                { m.routesCreated.Add(1) }
func (m *Metrics) incRoutesClosed()                 { m.routesClosed.Add(1) }
func (m *Metrics) incRoutesFailed()                 { m.routesFailed.Add(1) }
func (m *Metrics) incRegistrationsAdded()           { m.registrationsAdded.Add(1) }
func (m *Metrics) incRegistrationsDropped()         { m.registrationsDropped.Add(1) }
func (m *Metrics) incControlMessages()              { m.controlMessages.Add(1) }
func (m *Metrics) incControlErrors()                { m.controlErrors.Add(1) }
func (m *Metrics) incLivenessChecks()               { m.livenessChecks.Add(1) }
func (m *Metrics) incStaleRoutesExpired()           { m.staleRoutesExpired.Add(1) }
func (m *Metrics) incRoutesExpiredViaRegistration() { m.routesExpiredViaRegistration.Add(1) }

// Snapshot returns all counters as a map for diagnostic output.
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"routes_created":                  m.RoutesCreated(),
		"routes_closed":                   m.RoutesClosed(),
		"routes_failed":                   m.RoutesFailed(),
		"registrations_added":             m.RegistrationsAdded(),
		"registrations_dropped":           m.RegistrationsDropped(),
		"routes_expired_via_registration": m.RoutesExpiredViaRegistration(),
		"control_messages":                m.ControlMessages(),
		"control_errors":                  m.ControlErrors(),
		"liveness_checks":                 m.LivenessChecks(),
		"stale_routes_expired":            m.StaleRoutesExpired(),
	}
}

// ActiveRegistrations returns the current registration count from a Registry.
// This is a functional accessor, not a counter — it reads the Registry directly.
func (m *Metrics) ActiveRegistrations(r *Registry) int { return r.RegistrationCount() }

// ActiveRoutes returns the current route count from a Registry.
func (m *Metrics) ActiveRoutes(r *Registry) int { return r.RouteCount() }
