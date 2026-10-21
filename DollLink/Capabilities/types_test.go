package capabilities

import (
	"encoding/json"
	"testing"
)

func TestAdvertisementRoundTripAndValidation(t *testing.T) {
	want := NewAdvertisement("body-1", []Capability{{
		ID: "filesystem", Operations: []string{"read", "list"}, Available: true,
		Metadata: map[string]string{"scope": "home"},
	}})
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Advertisement
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if got.BodyID != want.BodyID || got.Payload.Capabilities[0].ID != "filesystem" {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestAdvertisementRejectsInvalidCapabilities(t *testing.T) {
	tests := []Advertisement{
		{},
		NewAdvertisement("body-1", []Capability{{ID: "", Operations: []string{"read"}}}),
		NewAdvertisement("body-1", []Capability{{ID: "filesystem"}}),
		NewAdvertisement("body-1", []Capability{
			{ID: "filesystem", Operations: []string{"read"}},
			{ID: "filesystem", Operations: []string{"list"}},
		}),
		NewAdvertisement("body-1", []Capability{{ID: "filesystem", Operations: []string{"read", "read"}}}),
	}
	for i, advertisement := range tests {
		if err := advertisement.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}
