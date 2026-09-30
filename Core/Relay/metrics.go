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

	udpDatagramsReceived            atomic.Int64
	udpDatagramsForwarded           atomic.Int64
	udpDatagramsDroppedOversized    atomic.Int64
	udpDatagramsDroppedUnknownRoute atomic.Int64
	udpDatagramsDroppedQueueFull    atomic.Int64
	udpDatagramsDroppedZeroLength   atomic.Int64
	udpDatagramsDroppedNoSink       atomic.Int64
	udpBytesIn                      atomic.Int64
	udpBytesOut                     atomic.Int64
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

// UDP ingress counters.
func (m *Metrics) UDPDatagramsReceived() int64         { return m.udpDatagramsReceived.Load() }
func (m *Metrics) UDPDatagramsForwarded() int64        { return m.udpDatagramsForwarded.Load() }
func (m *Metrics) UDPDatagramsDroppedOversized() int64 { return m.udpDatagramsDroppedOversized.Load() }
func (m *Metrics) UDPDatagramsDroppedUnknownRoute() int64 {
	return m.udpDatagramsDroppedUnknownRoute.Load()
}
func (m *Metrics) UDPDatagramsDroppedQueueFull() int64 { return m.udpDatagramsDroppedQueueFull.Load() }
func (m *Metrics) UDPDatagramsDroppedZeroLength() int64 {
	return m.udpDatagramsDroppedZeroLength.Load()
}
func (m *Metrics) UDPDatagramsDroppedNoSink() int64 { return m.udpDatagramsDroppedNoSink.Load() }
func (m *Metrics) UDPBytesIn() int64                { return m.udpBytesIn.Load() }
func (m *Metrics) UDPBytesOut() int64               { return m.udpBytesOut.Load() }

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

func (m *Metrics) incUDPDatagramsReceived()            { m.udpDatagramsReceived.Add(1) }
func (m *Metrics) incUDPDatagramsForwarded()           { m.udpDatagramsForwarded.Add(1) }
func (m *Metrics) incUDPDatagramsDroppedOversized()    { m.udpDatagramsDroppedOversized.Add(1) }
func (m *Metrics) incUDPDatagramsDroppedUnknownRoute() { m.udpDatagramsDroppedUnknownRoute.Add(1) }
func (m *Metrics) incUDPDatagramsDroppedQueueFull()    { m.udpDatagramsDroppedQueueFull.Add(1) }
func (m *Metrics) incUDPDatagramsDroppedZeroLength()   { m.udpDatagramsDroppedZeroLength.Add(1) }
func (m *Metrics) incUDPDatagramsDroppedNoSink()       { m.udpDatagramsDroppedNoSink.Add(1) }
func (m *Metrics) addUDPBytesIn(n int64)               { m.udpBytesIn.Add(n) }
func (m *Metrics) addUDPBytesOut(n int64)              { m.udpBytesOut.Add(n) }

// Snapshot returns all counters as a map for diagnostic output.
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"routes_created":                      m.RoutesCreated(),
		"routes_closed":                       m.RoutesClosed(),
		"routes_failed":                       m.RoutesFailed(),
		"registrations_added":                 m.RegistrationsAdded(),
		"registrations_dropped":               m.RegistrationsDropped(),
		"routes_expired_via_registration":     m.RoutesExpiredViaRegistration(),
		"control_messages":                    m.ControlMessages(),
		"control_errors":                      m.ControlErrors(),
		"liveness_checks":                     m.LivenessChecks(),
		"stale_routes_expired":                m.StaleRoutesExpired(),
		"udp_datagrams_received":              m.UDPDatagramsReceived(),
		"udp_datagrams_forwarded":             m.UDPDatagramsForwarded(),
		"udp_datagrams_dropped_oversized":     m.UDPDatagramsDroppedOversized(),
		"udp_datagrams_dropped_unknown_route": m.UDPDatagramsDroppedUnknownRoute(),
		"udp_datagrams_dropped_queue_full":    m.UDPDatagramsDroppedQueueFull(),
		"udp_datagrams_dropped_zero_length":   m.UDPDatagramsDroppedZeroLength(),
		"udp_datagrams_dropped_no_sink":       m.UDPDatagramsDroppedNoSink(),
		"udp_bytes_in":                        m.UDPBytesIn(),
		"udp_bytes_out":                       m.UDPBytesOut(),
	}
}

// ActiveRegistrations returns the current registration count from a Registry.
// This is a functional accessor, not a counter — it reads the Registry directly.
func (m *Metrics) ActiveRegistrations(r *Registry) int { return r.RegistrationCount() }

// ActiveRoutes returns the current route count from a Registry.
func (m *Metrics) ActiveRoutes(r *Registry) int { return r.RouteCount() }
