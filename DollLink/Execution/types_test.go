package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var wireTime = time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)

func validRequest() Request {
	return Request{
		Type:      TypeRequest,
		ID:        "evt_10",
		Timestamp: wireTime,
		DollID:    "doll_1",
		BodyID:    "body_1",
		Payload: RequestPayload{
			ExecutionID: "exec_123",
			Capability:  "filesystem",
			Operation:   "read",
			Arguments:   map[string]any{"path": "/tmp/hello.txt"},
		},
	}
}

func validResult() Result {
	return Result{
		Type:          TypeResult,
		ID:            "evt_11",
		Timestamp:     wireTime,
		DollID:        "doll_1",
		BodyID:        "body_1",
		CorrelationID: "evt_10",
		Payload: ResultPayload{
			ExecutionID: "exec_123",
			Status:      StatusSuccess,
			Result:      map[string]any{"content": "hello"},
		},
	}
}

func TestRequestCanonicalJSONRoundTrip(t *testing.T) {
	want := `{"type":"execution.request","id":"evt_10","timestamp":"2026-09-23T00:00:00Z","doll_id":"doll_1","body_id":"body_1","payload":{"execution_id":"exec_123","capability":"filesystem","operation":"read","arguments":{"path":"/tmp/hello.txt"}}}`
	data, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != want {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", data, want)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(DollLinkVersion, SenderCore); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestResultCanonicalJSONRoundTrip(t *testing.T) {
	want := `{"type":"execution.result","id":"evt_11","timestamp":"2026-09-23T00:00:00Z","doll_id":"doll_1","body_id":"body_1","correlation_id":"evt_10","payload":{"execution_id":"exec_123","status":"success","result":{"content":"hello"}}}`
	data, err := json.Marshal(validResult())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != want {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", data, want)
	}
	var decoded Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(DollLinkVersion, SenderBody); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := decoded.ValidateResponseTo(validRequest()); err != nil {
		t.Fatalf("ValidateResponseTo: %v", err)
	}
}

func TestCanonicalTerminalStatuses(t *testing.T) {
	statuses := []Status{StatusSuccess, StatusDenied, StatusUnsupported, StatusInvalid,
		StatusUnavailable, StatusFailed, StatusCancelled, StatusExpired}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			result := validResult()
			result.Payload.Status = status
			if err := result.Validate(DollLinkVersion, SenderBody); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidationRejectsWrongVersionAndSender(t *testing.T) {
	request := validRequest()
	if err := request.Validate(2, SenderCore); err == nil {
		t.Fatal("request accepted unnegotiated Doll Link version")
	}
	if err := request.Validate(DollLinkVersion, SenderBody); err == nil {
		t.Fatal("request accepted Body sender")
	}
	result := validResult()
	if err := result.Validate(DollLinkVersion, SenderCore); err == nil {
		t.Fatal("result accepted Core sender")
	}
}

func TestValidationRejectsMalformedMessages(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Request)
	}{
		{"type", func(r *Request) { r.Type = "unknown" }},
		{"id", func(r *Request) { r.ID = "" }},
		{"timestamp", func(r *Request) { r.Timestamp = time.Time{} }},
		{"non-utc timestamp", func(r *Request) { r.Timestamp = wireTime.In(time.FixedZone("offset", 3600)) }},
		{"body", func(r *Request) { r.BodyID = "" }},
		{"correlation", func(r *Request) { r.CorrelationID = "evt_prior" }},
		{"execution", func(r *Request) { r.Payload.ExecutionID = "" }},
		{"capability", func(r *Request) { r.Payload.Capability = "" }},
		{"operation", func(r *Request) { r.Payload.Operation = "" }},
		{"arguments", func(r *Request) { r.Payload.Arguments = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := validRequest()
			tt.edit(&request)
			if err := request.Validate(DollLinkVersion, SenderCore); err == nil {
				t.Fatal("Validate accepted malformed request")
			}
		})
	}

	result := validResult()
	result.Payload.Status = "pending"
	if err := result.Validate(DollLinkVersion, SenderBody); err == nil {
		t.Fatal("Validate accepted non-terminal status")
	}
	result = validResult()
	result.CorrelationID = ""
	if err := result.Validate(DollLinkVersion, SenderBody); err == nil {
		t.Fatal("Validate accepted result without correlation_id")
	}
}

func TestCorrelationValidation(t *testing.T) {
	request := validRequest()
	result := validResult()
	if err := result.ValidateResponseTo(request); err != nil {
		t.Fatalf("valid correlation rejected: %v", err)
	}
	result.Payload.ExecutionID = "exec_other"
	if err := result.ValidateResponseTo(request); err == nil {
		t.Fatal("mismatched execution_id accepted")
	}
}

func TestForwardCompatibleUnknownFields(t *testing.T) {
	wire := `{"type":"execution.request","id":"evt_10","timestamp":"2026-09-23T00:00:00Z","body_id":"body_1","future_envelope":true,"payload":{"execution_id":"exec_123","capability":"filesystem","operation":"read","arguments":{},"future_payload":true}}`
	var request Request
	if err := json.Unmarshal([]byte(wire), &request); err != nil {
		t.Fatalf("Unmarshal with additive fields: %v", err)
	}
	if err := request.Validate(DollLinkVersion, SenderCore); err != nil {
		t.Fatalf("Validate with additive fields: %v", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "future_") {
		t.Fatalf("unknown fields leaked into typed re-encoding: %s", data)
	}
}

func TestPayloadsMustBeJSONObjects(t *testing.T) {
	var request Request
	err := json.Unmarshal([]byte(`{"type":"execution.request","id":"evt","timestamp":"2026-09-23T00:00:00Z","body_id":"body","payload":{"execution_id":"exec","capability":"filesystem","operation":"read","arguments":[]}}`), &request)
	if err == nil {
		t.Fatal("array arguments accepted as a JSON object")
	}

	var result Result
	err = json.Unmarshal([]byte(`{"type":"execution.result","id":"evt2","timestamp":"2026-09-23T00:00:00Z","body_id":"body","correlation_id":"evt","payload":{"execution_id":"exec","status":"success","result":[]}}`), &result)
	if err == nil {
		t.Fatal("array result accepted as a JSON object")
	}
}
