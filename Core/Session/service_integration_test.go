package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func openService(t *testing.T, path string, clock session.Clock) (session.Service, persistence.Store) {
	t.Helper()
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sessionStore, ok := store.(session.Store)
	if !ok {
		store.Close()
		t.Fatal("persistence store does not implement session.Store")
	}
	return session.NewWithClock(sessionStore, clock), store
}

func TestLifecycleAndConversationResume(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	service, store := openService(t, filepath.Join(t.TempDir(), "core.db"), clock)
	defer store.Close()

	owner := session.Owner{Kind: session.OwnerGateway, InstanceID: "telegram-main"}
	created, err := service.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: "chat-42"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || created.Status != session.StatusActive || created.CreatedAt != clock.Now() || created.UpdatedAt != clock.Now() {
		t.Fatalf("unexpected created session: %+v", created)
	}

	lookedUp, err := service.Lookup(ctx, created.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if *lookedUp != *created {
		t.Fatalf("Lookup = %+v, want %+v", lookedUp, created)
	}

	clock.Advance(time.Minute)
	resumed, err := service.Resume(ctx, session.ResumeRequest{Owner: owner, ExternalConversationRef: "chat-42"})
	if err != nil {
		t.Fatalf("Resume by conversation: %v", err)
	}
	if resumed.ID != created.ID || resumed.UpdatedAt != clock.Now() {
		t.Fatalf("resumed session = %+v", resumed)
	}

	clock.Advance(time.Minute)
	closed, err := service.Close(ctx, created.ID)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.Status != session.StatusClosed || closed.UpdatedAt != clock.Now() {
		t.Fatalf("closed session = %+v", closed)
	}
	if _, err := service.Resume(ctx, session.ResumeRequest{SessionID: created.ID}); !errors.Is(err, session.ErrInvalidTransition) {
		t.Fatalf("Resume closed error = %v, want ErrInvalidTransition", err)
	}
	if _, err := service.Close(ctx, created.ID); !errors.Is(err, session.ErrInvalidTransition) {
		t.Fatalf("second Close error = %v, want ErrInvalidTransition", err)
	}

	restarted, err := service.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: "chat-42"})
	if err != nil {
		t.Fatalf("create replacement session: %v", err)
	}
	if restarted.ID == created.ID {
		t.Fatal("Session Restart reused the closed session ID")
	}
}

func TestPersistenceAndRestartReconstruction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	clock := &testClock{now: time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)}
	first, store := openService(t, path, clock)
	body, err := first.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-a"}})
	if err != nil {
		t.Fatalf("create Body session: %v", err)
	}
	gateway, err := first.Create(ctx, session.CreateRequest{
		Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "telegram-main"}, ExternalConversationRef: "chat-a",
	})
	if err != nil {
		t.Fatalf("create Gateway session: %v", err)
	}
	if _, err := first.Close(ctx, body.ID); err != nil {
		t.Fatalf("close Body session: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, reopened := openService(t, path, clock)
	defer reopened.Close()
	active, err := second.ListActive(ctx, nil)
	if err != nil {
		t.Fatalf("ListActive after restart: %v", err)
	}
	if len(active) != 1 || active[0].ID != gateway.ID {
		t.Fatalf("active after restart = %+v, want Gateway session %q", active, gateway.ID)
	}
	closed, err := second.Lookup(ctx, body.ID)
	if err != nil || closed.Status != session.StatusClosed {
		t.Fatalf("closed session after restart = %+v, %v", closed, err)
	}
	resumed, err := second.Resume(ctx, session.ResumeRequest{SessionID: gateway.ID})
	if err != nil || resumed.ID != gateway.ID {
		t.Fatalf("Resume after restart = %+v, %v", resumed, err)
	}
}

func TestConcurrentSessionsRemainIndependent(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)}
	service, store := openService(t, filepath.Join(t.TempDir(), "core.db"), clock)
	defer store.Close()

	const count = 48
	var wg sync.WaitGroup
	errs := make(chan error, count)
	ids := make(chan session.ID, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := service.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-shared"}})
			if err != nil {
				errs <- err
				return
			}
			ids <- created.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Errorf("concurrent Create: %v", err)
	}
	unique := make(map[session.ID]struct{})
	for id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) != count {
		t.Fatalf("created %d unique sessions, want %d", len(unique), count)
	}
	active, err := service.ListActive(ctx, &session.ActiveFilter{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-shared"}})
	if err != nil || len(active) != count {
		t.Fatalf("ListActive = %d sessions, %v; want %d", len(active), err, count)
	}

	var created atomic.Int32
	errResults := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Create(ctx, session.CreateRequest{
				Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway-one"}, ExternalConversationRef: "same-chat",
			})
			if err == nil {
				created.Add(1)
			} else {
				errResults <- err
			}
		}()
	}
	wg.Wait()
	close(errResults)
	if created.Load() != 1 {
		t.Fatalf("successful duplicate conversation creates = %d, want 1", created.Load())
	}
	for err := range errResults {
		if !errors.Is(err, session.ErrConversationInUse) {
			t.Errorf("duplicate conversation error = %v", err)
		}
	}
}

func TestInvalidOperations(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)}
	service, store := openService(t, filepath.Join(t.TempDir(), "core.db"), clock)
	defer store.Close()

	invalidCreates := []session.CreateRequest{
		{},
		{Owner: session.Owner{Kind: "platform", InstanceID: "one"}},
		{Owner: session.Owner{Kind: session.OwnerGateway}},
	}
	for _, request := range invalidCreates {
		if _, err := service.Create(ctx, request); !errors.Is(err, session.ErrInvalidRequest) {
			t.Errorf("Create(%+v) error = %v", request, err)
		}
	}
	if _, err := service.Lookup(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Lookup missing error = %v", err)
	}
	if _, err := service.Resume(ctx, session.ResumeRequest{}); !errors.Is(err, session.ErrInvalidRequest) {
		t.Fatalf("empty Resume error = %v", err)
	}
	if _, err := service.Resume(ctx, session.ResumeRequest{
		SessionID: "one", Owner: session.Owner{Kind: session.OwnerGateway, InstanceID: "gateway"}, ExternalConversationRef: "chat",
	}); !errors.Is(err, session.ErrInvalidRequest) {
		t.Fatalf("ambiguous Resume error = %v", err)
	}
	if _, err := service.Close(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Close missing error = %v", err)
	}
	if _, err := service.ListActive(ctx, &session.ActiveFilter{}); !errors.Is(err, session.ErrInvalidRequest) {
		t.Fatalf("invalid ListActive filter error = %v", err)
	}
}
