package permissionrequest_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type fixture struct {
	t       *testing.T
	path    string
	store   persistence.Store
	clock   *clock
	policy  actionpolicy.Policy
	service *permissionrequest.Manager
}

func newFixture(t *testing.T) *fixture {
	return newFixtureWithPolicy(t, actionpolicy.Default())
}

func newFixtureWithPolicy(t *testing.T, policy actionpolicy.Policy) *fixture {
	t.Helper()
	fx := &fixture{t: t, path: filepath.Join(t.TempDir(), "core.db"), policy: policy, clock: &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}}
	fx.reopen()
	t.Cleanup(func() { _ = fx.store.Close() })
	return fx
}

func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	store, err := persistence.NewStore(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = store
	permissionStore, ok := store.(permissionrequest.Store)
	if !ok {
		f.t.Fatal("persistence store does not implement permission request store")
	}
	f.service = permissionrequest.NewWithClock(permissionStore, f.policy, f.clock)
}

func (f *fixture) create(origin permissionrequest.Origin) *permissionrequest.Request {
	f.t.Helper()
	input := permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: origin, Action: "read runtime information",
		Capability: "runtime.info", Operation: "read", Arguments: json.RawMessage(`{"field":"status"}`),
		ExpiresAt:   f.clock.now.Add(time.Hour),
		Correlation: permissionrequest.Correlation{CognitiveRunID: "run-1", ToolCallID: "call-1"},
	}
	value, err := f.service.Create(context.Background(), input)
	if err != nil {
		f.t.Fatal(err)
	}
	return value
}

func TestCreateVoluntaryAndPolicyRequired(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Capability: "runtime.info", Operation: "read", Decision: actionpolicy.DecisionRequireApproval,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureWithPolicy(t, policy)
	voluntary, err := fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: permissionrequest.OriginVoluntary,
		Question: "May I contact the owner?", ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if err != nil || voluntary.Status != permissionrequest.StatusPending || voluntary.Question == "" {
		t.Fatalf("voluntary create = %#v, %v", voluntary, err)
	}
	request := fx.create(permissionrequest.OriginPolicyRequired)
	if request.Capability != "runtime.info" || request.Operation != "read" {
		t.Fatalf("policy request = %#v", request)
	}

}

func TestProhibitedActionCannotBeCreatedAsPolicyRequired(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Capability: "network", Operation: "publish", Decision: actionpolicy.DecisionDeny,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureWithPolicy(t, policy)
	_, err = fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: permissionrequest.OriginPolicyRequired,
		Action: "publish network data", Capability: "network", Operation: "publish",
		ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if !errors.Is(err, permissionrequest.ErrInvalidRequest) {
		t.Fatalf("prohibited policy-required action error = %v", err)
	}
}

func TestVoluntaryActionCannotBypassProhibition(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Capability: "network", Operation: "publish", Decision: actionpolicy.DecisionDeny,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureWithPolicy(t, policy)
	_, err = fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: permissionrequest.OriginVoluntary,
		Action: "publish network data", Question: "Should I publish this?", Capability: "network", Operation: "publish",
		ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if !errors.Is(err, permissionrequest.ErrInvalidRequest) {
		t.Fatalf("prohibited voluntary action error = %v", err)
	}
}

func TestValidPolicyRequiredRequestUsesTrustedPolicy(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Doll: "doll-1", Capability: "network", Operation: "publish", Decision: actionpolicy.DecisionRequireApproval,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureWithPolicy(t, policy)
	got, err := fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: permissionrequest.OriginPolicyRequired,
		Action: "publish network data", Capability: "network", Operation: "publish",
		ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if err != nil || got.Status != permissionrequest.StatusPending {
		t.Fatalf("policy-required create = %#v, %v", got, err)
	}
}

func TestUnrestrictedVoluntaryQuestion(t *testing.T) {
	fx := newFixture(t)
	got, err := fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll-1", OwnerID: "owner-1", Origin: permissionrequest.OriginVoluntary,
		Question: "Would you prefer the blue option?", ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if err != nil || got.Question == "" || got.Status != permissionrequest.StatusPending {
		t.Fatalf("voluntary question = %#v, %v", got, err)
	}
}

func TestPersistenceAndCorrelationAcrossRestart(t *testing.T) {
	policy, _ := actionpolicy.New([]actionpolicy.Rule{{Capability: "runtime.info", Operation: "read", Decision: actionpolicy.DecisionRequireApproval}})
	fx := newFixtureWithPolicy(t, policy)
	created := fx.create(permissionrequest.OriginPolicyRequired)
	fx.reopen()
	loaded, err := fx.service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Correlation.CognitiveRunID != "run-1" || loaded.Correlation.ToolCallID != "call-1" || string(loaded.Arguments) != `{"field":"status"}` {
		t.Fatalf("loaded request lost correlation: %#v", loaded)
	}
	pending, err := fx.service.ListPending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].ID != created.ID {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}

func TestApprovalDenialAndIdempotence(t *testing.T) {
	policy, _ := actionpolicy.New([]actionpolicy.Rule{{Capability: "runtime.info", Operation: "read", Decision: actionpolicy.DecisionRequireApproval}})
	fx := newFixtureWithPolicy(t, policy)
	approved := fx.create(permissionrequest.OriginPolicyRequired)
	got, err := fx.service.Approve(context.Background(), approved.ID, "owner-1")
	if err != nil || got.Status != permissionrequest.StatusApproved || got.ResolvedAt == nil || got.ResolvingAuthority != "owner-1" {
		t.Fatalf("approve = %#v, %v", got, err)
	}
	if _, err := fx.service.Approve(context.Background(), approved.ID, "owner-1"); err != nil {
		t.Fatalf("idempotent approve: %v", err)
	}
	if _, err := fx.service.Deny(context.Background(), approved.ID, "owner-1"); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("conflicting denial error = %v", err)
	}
	duplicate, err := fx.service.Approve(context.Background(), approved.ID, "owner-2")
	if err != nil || duplicate.ResolvingAuthority != "owner-1" {
		t.Fatalf("idempotent approval changed resolver: %#v, %v", duplicate, err)
	}

	denied := fx.create(permissionrequest.OriginPolicyRequired)
	if got, err := fx.service.Deny(context.Background(), denied.ID, "owner-1"); err != nil || got.Status != permissionrequest.StatusDenied {
		t.Fatalf("deny = %#v, %v", got, err)
	}
}

func TestCancellationExpirationAndInvalidTransitions(t *testing.T) {
	fx := newFixture(t)
	cancelled := fx.create(permissionrequest.OriginVoluntary)
	if got, err := fx.service.Cancel(context.Background(), cancelled.ID); err != nil || got.Status != permissionrequest.StatusCancelled {
		t.Fatalf("cancel = %#v, %v", got, err)
	}
	if _, err := fx.service.Approve(context.Background(), cancelled.ID, "owner-1"); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("approve cancelled error = %v", err)
	}

	expired := fx.create(permissionrequest.OriginVoluntary)
	if _, err := fx.service.Expire(context.Background(), expired.ID); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("early expire error = %v", err)
	}
	fx.clock.now = fx.clock.now.Add(2 * time.Hour)
	got, err := fx.service.Expire(context.Background(), expired.ID)
	if err != nil || got.Status != permissionrequest.StatusExpired || got.ResolvedAt == nil {
		t.Fatalf("expire = %#v, %v", got, err)
	}
	if _, err := fx.service.Expire(context.Background(), expired.ID); err != nil {
		t.Fatalf("idempotent expire: %v", err)
	}
	if _, err := fx.service.Cancel(context.Background(), expired.ID); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("cancel expired error = %v", err)
	}
}

func TestConcurrentConflictingResolutionHasOneWinner(t *testing.T) {
	policy, _ := actionpolicy.New([]actionpolicy.Rule{{Capability: "runtime.info", Operation: "read", Decision: actionpolicy.DecisionRequireApproval}})
	fx := newFixtureWithPolicy(t, policy)
	request := fx.create(permissionrequest.OriginPolicyRequired)

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, approve := range []bool{true, false} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var err error
			if approve {
				_, err = fx.service.Approve(context.Background(), request.ID, "owner-1")
			} else {
				_, err = fx.service.Deny(context.Background(), request.ID, "owner-1")
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, permissionrequest.ErrInvalidTransition):
			conflicted++
		default:
			t.Fatalf("unexpected resolution error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("successes = %d, conflicts = %d", succeeded, conflicted)
	}
}

func TestExpiredRequestsAreDurablyReconciled(t *testing.T) {
	fx := newFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	fx.clock.now = request.ExpiresAt

	pending, err := fx.service.ListPending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	fx.reopen()
	got, err := fx.service.Get(context.Background(), request.ID)
	if err != nil || got.Status != permissionrequest.StatusExpired || got.ResolvedAt == nil {
		t.Fatalf("reloaded expired request = %#v, %v", got, err)
	}
}

func TestValidation(t *testing.T) {
	fx := newFixture(t)
	for name, input := range map[string]permissionrequest.CreateRequest{
		"missing identity":  {Origin: permissionrequest.OriginVoluntary, Question: "why", ExpiresAt: fx.clock.now.Add(time.Hour)},
		"past expiry":       {DollID: "d", OwnerID: "o", Origin: permissionrequest.OriginVoluntary, Question: "why", ExpiresAt: fx.clock.now},
		"missing content":   {DollID: "d", OwnerID: "o", Origin: permissionrequest.OriginVoluntary, ExpiresAt: fx.clock.now.Add(time.Hour)},
		"bad arguments":     {DollID: "d", OwnerID: "o", Origin: permissionrequest.OriginVoluntary, Action: "a", Arguments: json.RawMessage(`{`), ExpiresAt: fx.clock.now.Add(time.Hour)},
		"unrequired policy": {DollID: "d", OwnerID: "o", Origin: permissionrequest.OriginPolicyRequired, Action: "a", Capability: "c", Operation: "o", ExpiresAt: fx.clock.now.Add(time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fx.service.Create(context.Background(), input); !errors.Is(err, permissionrequest.ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
