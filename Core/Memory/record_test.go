package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixtureRecord() Record {
	return Record{SchemaVersion: 1, ID: "mem-1", Revision: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Source: Source{Type: "interaction", Ref: "turn-1"}, Content: Content{MediaType: "text/plain; charset=utf-8", Transport: TransportInline, Data: json.RawMessage(`"hello"`)}, Metadata: Metadata{Kind: "future-provider-kind"}}
}

func TestRecordRoundTripAndOpenVocabularies(t *testing.T) {
	record := fixtureRecord()
	data, err := EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != record.ID || got.Metadata.Kind != "future-provider-kind" || !strings.Contains(string(data), `"experienced_at":null`) {
		t.Fatalf("unexpected round trip: %s", data)
	}
}

func TestRecordContentTransports(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Record)
		ok     bool
	}{
		{"json inline", func(r *Record) { r.Content.MediaType = "application/json"; r.Content.Data = json.RawMessage(`{"x":1}`) }, true},
		{"binary stream", func(r *Record) {
			r.Content.MediaType = "image/png"
			r.Content.Transport = TransportStream
			r.Content.Data = nil
		}, true},
		{"binary inline", func(r *Record) { r.Content.MediaType = "image/png" }, false},
		{"stream data", func(r *Record) { r.Content.Transport = TransportStream }, false},
		{"unknown transport", func(r *Record) { r.Content.Transport = "magic" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureRecord()
			tc.change(&r)
			err := r.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDecodeRequiresCompleteStrictEnvelope(t *testing.T) {
	data, _ := EncodeRecord(fixtureRecord())
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	delete(object, "experienced_at")
	missing, _ := json.Marshal(object)
	if _, err := DecodeRecord(missing); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("missing field: %v", err)
	}
	object["experienced_at"] = nil
	object["surprise"] = true
	unknown, _ := json.Marshal(object)
	if _, err := DecodeRecord(unknown); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestRecordRevisionAndProvenanceValidation(t *testing.T) {
	for _, mutate := range []func(*Record){func(r *Record) { r.Revision = 0 }, func(r *Record) { r.Source = Source{Type: "unknown", Ref: "invented"} }, func(r *Record) { r.DerivedFrom = []string{r.ID} }} {
		r := fixtureRecord()
		mutate(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("expected invalid, got %v", err)
		}
	}
}
