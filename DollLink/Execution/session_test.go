package execution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type requestRecorder struct {
	requests chan Request
}

func (r *requestRecorder) SendExecutionRequest(_ context.Context, request Request) error {
	r.requests <- request
	return nil
}

func newTestClient(t *testing.T) (*Client, *requestRecorder) {
	t.Helper()
	recorder := &requestRecorder{requests: make(chan Request, 10)}
	client, err := NewClient("body-1", recorder)
	if err != nil {
		t.Fatal(err)
	}
	return client, recorder
}

func testPayload(id string) RequestPayload {
	return RequestPayload{ExecutionID: id, Capability: "lights", Operation: "set", Arguments: map[string]any{"on": true}}
}

func resultFor(request Request) Result {
	return Result{
		Type:          TypeResult,
		ID:            "result-" + request.Payload.ExecutionID,
		Timestamp:     time.Now().UTC(),
		DollID:        request.DollID,
		BodyID:        request.BodyID,
		CorrelationID: request.ID,
		Payload: ResultPayload{
			ExecutionID: request.Payload.ExecutionID,
			Status:      StatusSuccess,
			Result:      map[string]any{"ok": true},
		},
	}
}

func TestClientConcurrentOutOfOrderResults(t *testing.T) {
	client, recorder := newTestClient(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.Execute(context.Background(), "doll-1", testPayload(id))
			if err == nil && result.Payload.ExecutionID != id {
				err = errors.New("result delivered to wrong request")
			}
			errs <- err
		}()
	}
	first, second := <-recorder.requests, <-recorder.requests
	if err := client.HandleResult(resultFor(second)); err != nil {
		t.Fatal(err)
	}
	if err := client.HandleResult(resultFor(first)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientRejectsMismatchedResultsWithoutConsumingRequest(t *testing.T) {
	client, recorder := newTestClient(t)
	done := make(chan error, 1)
	go func() {
		_, err := client.Execute(context.Background(), "doll-1", testPayload("exec-1"))
		done <- err
	}()
	request := <-recorder.requests

	wrongBody := resultFor(request)
	wrongBody.BodyID = "body-2"
	if err := client.HandleResult(wrongBody); err == nil {
		t.Fatal("mismatched Body identity was accepted")
	}
	wrongExecution := resultFor(request)
	wrongExecution.Payload.ExecutionID = "other"
	if err := client.HandleResult(wrongExecution); err == nil {
		t.Fatal("mismatched execution ID was accepted")
	}
	unsolicited := resultFor(request)
	unsolicited.CorrelationID = "unknown"
	if err := client.HandleResult(unsolicited); err == nil {
		t.Fatal("unsolicited result was accepted")
	}
	if err := client.HandleResult(resultFor(request)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientCancellationTimeoutAndDisconnectCleanup(t *testing.T) {
	client, recorder := newTestClient(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, err := client.Execute(ctx, "", testPayload("cancel"))
		cancelled <- err
	}()
	<-recorder.requests
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer timeoutCancel()
	timedOut := make(chan error, 1)
	go func() {
		_, err := client.Execute(timeoutCtx, "", testPayload("timeout"))
		timedOut <- err
	}()
	<-recorder.requests
	if err := <-timedOut; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}

	disconnected := make(chan error, 1)
	go func() {
		_, err := client.Execute(context.Background(), "", testPayload("disconnect"))
		disconnected <- err
	}()
	request := <-recorder.requests
	client.Close(ErrDisconnected)
	if err := <-disconnected; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnect error = %v", err)
	}
	if err := client.HandleResult(resultFor(request)); err == nil {
		t.Fatal("late result was accepted after disconnect cleanup")
	}
	if _, err := client.Execute(context.Background(), "", testPayload("late")); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("closed session error = %v", err)
	}
}
