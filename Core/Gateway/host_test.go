package gateway

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

type concurrencyAdapter struct {
	id             externalidentity.GatewayInstanceID
	startEntered   chan struct{}
	startRelease   chan struct{}
	deliverEntered chan struct{}
	deliverRelease chan struct{}
	startErr       error
	deliverErr     error
	onStart        func()
	onDeliver      func()
	onStop         func()
	starts         atomic.Int32
	deliveries     atomic.Int32
	stops          atomic.Int32
}

func (a *concurrencyAdapter) InstanceID() externalidentity.GatewayInstanceID { return a.id }
func (*concurrencyAdapter) ABIVersion() int                                  { return StructuredABIVersion }
func (a *concurrencyAdapter) Start(context.Context, Ingress) error {
	a.starts.Add(1)
	if a.startEntered != nil {
		close(a.startEntered)
	}
	if a.onStart != nil {
		a.onStart()
	}
	if a.startRelease != nil {
		<-a.startRelease
	}
	return a.startErr
}
func (a *concurrencyAdapter) Deliver(context.Context, OutboundEnvelope) error {
	a.deliveries.Add(1)
	if a.deliverEntered != nil {
		close(a.deliverEntered)
	}
	if a.onDeliver != nil {
		a.onDeliver()
	}
	if a.deliverRelease != nil {
		<-a.deliverRelease
	}
	return a.deliverErr
}
func (a *concurrencyAdapter) Stop(context.Context) error {
	a.stops.Add(1)
	if a.onStop != nil {
		a.onStop()
	}
	return nil
}

func configuredHost(t *testing.T) *Host {
	t.Helper()
	host := NewHost()
	if err := host.Configure(func(externalidentity.GatewayInstanceID) (*interaction.GatewayIngress, error) {
		return &interaction.GatewayIngress{}, nil
	}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return host
}

func testDelivery() (session.Owner, sessionevent.Delivery) {
	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}
	return owner, sessionevent.Delivery{ID: "delivery-one", Owner: owner}
}

func await(t *testing.T, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitRemoved(t *testing.T, host *Host, id externalidentity.GatewayInstanceID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		host.mu.Lock()
		_, exists := host.adapters[id]
		host.mu.Unlock()
		if !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for lifecycle exclusion")
		}
		runtime.Gosched()
	}
}

func awaitClosed(t *testing.T, host *Host) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		host.mu.Lock()
		closed := host.closed
		host.mu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for shutdown exclusion")
		}
		runtime.Gosched()
	}
}

func TestConcurrentRegisterReservesInstanceBeforeStart(t *testing.T) {
	host := configuredHost(t)
	first := &concurrencyAdapter{id: "gateway-one", startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	firstResult := make(chan error, 1)
	go func() { firstResult <- host.Register(context.Background(), first) }()
	await(t, first.startEntered, "first Start")
	second := &concurrencyAdapter{id: "gateway-one"}
	if err := host.Register(context.Background(), second); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register error = %v", err)
	}
	if second.starts.Load() != 0 {
		t.Fatalf("duplicate adapter Start calls = %d", second.starts.Load())
	}
	close(first.startRelease)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryRacingUnregisterDrainsBeforeStop(t *testing.T) {
	host := configuredHost(t)
	adapter := &concurrencyAdapter{id: "gateway-one", deliverEntered: make(chan struct{}), deliverRelease: make(chan struct{})}
	if err := host.Register(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}
	owner, delivery := testDelivery()
	deliverResult := make(chan error, 1)
	go func() { deliverResult <- host.DeliverSessionEvent(context.Background(), owner, delivery) }()
	await(t, adapter.deliverEntered, "Deliver")
	unregisterResult := make(chan error, 1)
	go func() { unregisterResult <- host.Unregister(context.Background(), "gateway-one") }()
	awaitRemoved(t, host, "gateway-one")
	if err := host.DeliverSessionEvent(context.Background(), owner, delivery); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("delivery after unregister began = %v", err)
	}
	if adapter.stops.Load() != 0 {
		t.Fatal("adapter stopped before in-flight delivery completed")
	}
	close(adapter.deliverRelease)
	if err := <-deliverResult; err != nil {
		t.Fatal(err)
	}
	if err := <-unregisterResult; err != nil {
		t.Fatal(err)
	}
	if adapter.stops.Load() != 1 {
		t.Fatalf("Stop calls = %d", adapter.stops.Load())
	}
}

func TestShutdownDuringStartupWaitsAndRejectsRegistration(t *testing.T) {
	host := configuredHost(t)
	adapter := &concurrencyAdapter{id: "gateway-one", startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	registerResult := make(chan error, 1)
	go func() { registerResult <- host.Register(context.Background(), adapter) }()
	await(t, adapter.startEntered, "Start")
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- host.Shutdown(context.Background()) }()
	awaitClosed(t, host)
	if err := host.Register(context.Background(), &concurrencyAdapter{id: "gateway-two"}); !errors.Is(err, ErrHostNotConfigured) {
		t.Fatalf("registration during shutdown error = %v", err)
	}
	close(adapter.startRelease)
	if err := <-shutdownResult; err != nil {
		t.Fatal(err)
	}
	if err := <-registerResult; !errors.Is(err, ErrHostNotConfigured) {
		t.Fatalf("startup result = %v", err)
	}
	if adapter.stops.Load() != 1 {
		t.Fatalf("Stop calls = %d", adapter.stops.Load())
	}
}

func TestAdapterCallbacksReenterHostWithoutDeadlock(t *testing.T) {
	host := configuredHost(t)
	secondary := &concurrencyAdapter{id: "gateway-two"}
	callbackDone := make(chan struct{})
	primary := &concurrencyAdapter{id: "gateway-one"}
	primary.onDeliver = func() {
		if err := host.Register(context.Background(), secondary); err != nil {
			t.Errorf("reentrant Register: %v", err)
		}
		close(callbackDone)
	}
	if err := host.Register(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	owner, delivery := testDelivery()
	if err := host.DeliverSessionEvent(context.Background(), owner, delivery); err != nil {
		t.Fatal(err)
	}
	await(t, callbackDone, "reentrant callback")
	primary.onStop = func() {
		if err := host.DeliverSessionEvent(context.Background(), owner, delivery); !errors.Is(err, ErrUnavailable) {
			t.Errorf("delivery during Stop error = %v", err)
		}
	}
	if err := host.Unregister(context.Background(), "gateway-one"); err != nil {
		t.Fatal(err)
	}
}

func TestSlowAndFailingDeliveryDoesNotBlockHostState(t *testing.T) {
	host := configuredHost(t)
	want := errors.New("adapter delivery failed")
	adapter := &concurrencyAdapter{id: "gateway-one", deliverErr: want}
	if err := host.Register(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}
	owner, delivery := testDelivery()
	if err := host.DeliverSessionEvent(context.Background(), owner, delivery); !errors.Is(err, want) {
		t.Fatalf("Deliver error = %v", err)
	}
	if err := host.Unregister(context.Background(), "gateway-one"); err != nil {
		t.Fatal(err)
	}
	if adapter.stops.Load() != 1 || adapter.deliveries.Load() != 1 {
		t.Fatalf("deliveries=%d stops=%d", adapter.deliveries.Load(), adapter.stops.Load())
	}
}

func TestFailedStartupReleasesReservation(t *testing.T) {
	host := configuredHost(t)
	want := errors.New("start failed")
	failed := &concurrencyAdapter{id: "gateway-one", startErr: want}
	if err := host.Register(context.Background(), failed); !errors.Is(err, want) {
		t.Fatalf("failed Register error = %v", err)
	}
	if failed.stops.Load() != 1 {
		t.Fatalf("failed adapter Stop calls = %d", failed.stops.Load())
	}
	if err := host.Register(context.Background(), &concurrencyAdapter{id: "gateway-one"}); err != nil {
		t.Fatalf("replacement Register: %v", err)
	}
}
