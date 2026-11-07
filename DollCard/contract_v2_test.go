package dollcard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Memory"
)

var testBounds = ImportBounds{MaxSections: 32, MaxEntries: 100, MaxDeclaredBytes: 1 << 20}

func contractEntry(name string, data []byte) ArchiveEntry {
	sum := sha256.Sum256(data)
	return ArchiveEntry{Path: name, SHA256: hex.EncodeToString(sum[:]), Size: uint64(len(data))}
}

func v2IndexFixture() IndexV2 {
	return IndexV2{Version: 2, DollID: "doll-1", Sections: []SectionIndex{
		{ID: "identity", Kind: SectionIdentity, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("identity.json", []byte("identity"))}},
		{ID: "soul", Kind: SectionSoul, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("soul.md", []byte("soul"))}},
		{ID: "self", Kind: SectionSelf, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("self/self.md", []byte("latest self"))}},
		{ID: "avatar", Kind: SectionAvatar, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "preferences", Kind: SectionPreferences, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "secrets", Kind: SectionSecrets, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("secrets/secrets.json", []byte("[]"))}},
		{ID: "skills", Kind: SectionSkills, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("skills/catalog.json", []byte("catalog")), contractEntry("skills/research/SKILL.md", []byte("instructions")), contractEntry("skills/research/assets/icon.png", []byte{0, 1, 2})}},
		{ID: "memories", Kind: SectionMemories, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("memories/memories-000001.jsonl", []byte("record\n"))}},
		{ID: "peers", Kind: SectionPeers, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("peers/identities.json", []byte("[]")), contractEntry("peers/primary-events.json", []byte("[]")), contractEntry("peers/descriptions/peer-a.json", []byte("description"))}},
		{ID: "authority", Kind: SectionAuthority, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("authority/decisions.json", []byte("[]")), contractEntry("authority/delegations.json", []byte("[]")), contractEntry("authority/consumptions.json", []byte("[]"))}},
		{ID: "drives", Kind: SectionDrives, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "goals", Kind: SectionGoals, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "intentions", Kind: SectionIntentions, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "extensions", Kind: SectionExtensions, SchemaVersion: 1, Required: true, Empty: true},
		{ID: "legacy-owner", Kind: SectionLegacyOwner, SchemaVersion: 1, Required: true, Entries: []ArchiveEntry{contractEntry("legacy/owner/owner.json", []byte("{}"))}},
		{ID: "legacy-auth", Kind: SectionLegacyAuth, SchemaVersion: 1, Required: true, Empty: true},
	}}
}

func sectionOfKind(t *testing.T, index IndexV2, kind SectionKind) SectionIndex {
	t.Helper()
	for _, section := range index.Sections {
		if section.Kind == kind {
			return section
		}
	}
	t.Fatalf("missing section %q", kind)
	return SectionIndex{}
}

func TestVersion2IndexParsingAndIntegrity(t *testing.T) {
	index := v2IndexFixture()
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseContractIndex(data, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != 2 || parsed.V2 == nil || parsed.Report.Status != CompatibilityCompatible {
		t.Fatalf("parsed=%+v", parsed)
	}
	asset := sectionOfKind(t, index, SectionSkills).Entries[2]
	if err := ValidateEntryData(asset, []byte{0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEntryData(asset, []byte{0, 1, 3}); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("integrity: %v", err)
	}
}

func TestVersion2DistinguishesEmptyFromMissingSections(t *testing.T) {
	index := v2IndexFixture()
	if err := index.Validate(testBounds); err != nil {
		t.Fatalf("explicitly empty sections should validate: %v", err)
	}

	for _, omittedKind := range requiredV2Sections {
		missing := v2IndexFixture()
		for i, section := range missing.Sections {
			if section.Kind == omittedKind {
				missing.Sections = append(missing.Sections[:i], missing.Sections[i+1:]...)
				break
			}
		}
		data, _ := json.Marshal(missing)
		parsed, err := ParseContractIndex(data, testBounds)
		if err != nil {
			t.Fatalf("omit %s: %v", omittedKind, err)
		}
		if parsed.Report.Status != CompatibilityIncompatible || !hasIssue(parsed.Report, "missing_section", string(omittedKind)) {
			t.Fatalf("missing %s was not reported: %+v", omittedKind, parsed.Report)
		}
	}

	invalid := v2IndexFixture()
	invalid.Sections[3].Empty = false
	if err := invalid.Validate(testBounds); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("implicit empty section accepted: %v", err)
	}
	invalid = v2IndexFixture()
	invalid.Sections[3].Entries = []ArchiveEntry{contractEntry("avatar/avatar.png", []byte("avatar"))}
	if err := invalid.Validate(testBounds); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("section marked empty with entries accepted: %v", err)
	}
}

func TestUnknownFieldsSurviveSemanticRoundTrip(t *testing.T) {
	data, err := json.Marshal(v2IndexFixture())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["future_top_level"] = map[string]any{"nested": []any{float64(1), "two", map[string]any{"enabled": true}}}
	sections := raw["sections"].([]any)
	sections[0].(map[string]any)["future_section_field"] = map[string]any{"values": []any{"future", "metadata"}}
	entries := sections[0].(map[string]any)["entries"].([]any)
	entries[0].(map[string]any)["future_integrity"] = map[string]any{"algorithm": "future-hash", "parameters": []any{float64(4), float64(8)}}
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseContractIndex(data, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Report.Status != CompatibilityCompatible {
		t.Fatalf("preservable unknown fields changed compatibility: %+v", parsed.Report)
	}
	roundTrip, err := json.Marshal(parsed.V2)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseContractIndex(roundTrip, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if !rawJSONEqual(parsed.V2.UnknownFields["future_top_level"], reparsed.V2.UnknownFields["future_top_level"]) {
		t.Fatal("unknown top-level JSON changed")
	}
	if !rawJSONEqual(parsed.V2.Sections[0].UnknownFields["future_section_field"], reparsed.V2.Sections[0].UnknownFields["future_section_field"]) {
		t.Fatal("unknown section JSON changed")
	}
	if !rawJSONEqual(parsed.V2.Sections[0].Entries[0].UnknownFields["future_integrity"], reparsed.V2.Sections[0].Entries[0].UnknownFields["future_integrity"]) {
		t.Fatal("unknown entry JSON changed")
	}
}

func TestMultipleUnknownSectionsRemainPreservable(t *testing.T) {
	index := v2IndexFixture()
	optional := SectionIndex{
		ID: "future-one", Kind: "future_state", SchemaVersion: 7,
		Entries:       []ArchiveEntry{contractEntry("future/one.bin", []byte{0, 255, 1})},
		UnknownFields: UnknownFields{"future_metadata": json.RawMessage(`{"nested":[1,{"two":true}]}`)},
	}
	optional.Entries[0].UnknownFields = UnknownFields{"future_hash": json.RawMessage(`{"kind":"opaque"}`)}
	optionalTwo := SectionIndex{
		ID: "future-three", Kind: "another_future_state", SchemaVersion: 1,
		Entries:       []ArchiveEntry{contractEntry("future/three.dat", []byte("another optional section"))},
		UnknownFields: UnknownFields{"labels": json.RawMessage(`["one","two"]`)},
	}
	required := SectionIndex{
		ID: "future-two", Kind: "future_required", SchemaVersion: 2, Required: true,
		Entries:       []ArchiveEntry{contractEntry("future/two.dat", []byte("required"))},
		UnknownFields: UnknownFields{"activation": json.RawMessage(`{"mode":"future"}`)},
	}
	index.Sections = append(index.Sections, optional, optionalTwo, required)
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseContractIndex(data, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Report.Status != CompatibilityIncompatible || !hasIssue(parsed.Report, "unknown_optional_section", "future-one") || !hasIssue(parsed.Report, "unknown_optional_section", "future-three") || !hasIssue(parsed.Report, "unknown_required_section", "future-two") {
		t.Fatalf("report=%+v", parsed.Report)
	}
	encoded, err := json.Marshal(parsed.V2)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped IndexV2
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if len(roundTripped.Sections) != len(index.Sections) || !rawJSONEqual(roundTripped.Sections[len(roundTripped.Sections)-3].UnknownFields["future_metadata"], optional.UnknownFields["future_metadata"]) || !rawJSONEqual(roundTripped.Sections[len(roundTripped.Sections)-2].UnknownFields["labels"], optionalTwo.UnknownFields["labels"]) || !rawJSONEqual(roundTripped.Sections[len(roundTripped.Sections)-1].UnknownFields["activation"], required.UnknownFields["activation"]) {
		t.Fatal("unknown sections were not retained")
	}
}

func TestUnknownPayloadBytesArePreservedExactly(t *testing.T) {
	one := []byte{0, 1, 2, 0, 255, 128}
	two := []byte("\x00future\nbytes\xff")
	index := IndexV2{Sections: []SectionIndex{{
		ID: "future", Kind: "future_state", SchemaVersion: 1, Required: true,
		Entries: []ArchiveEntry{contractEntry("future/one.bin", one), contractEntry("future/two.bin", two)},
	}}}
	preserved, err := PreserveIndexedPayloads(index, map[string][]byte{"future/one.bin": one, "future/two.bin": two})
	if err != nil {
		t.Fatal(err)
	}
	one[0] = 99
	if !bytes.Equal(preserved["future/one.bin"], []byte{0, 1, 2, 0, 255, 128}) || !bytes.Equal(preserved["future/two.bin"], two) {
		t.Fatalf("payload changed: %#v", preserved)
	}
	if _, err := PreserveIndexedPayloads(index, map[string][]byte{"future/one.bin": one, "future/two.bin": two}); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("integrity failure not rejected: %v", err)
	}
}

func TestDuplicateJSONKeysAreRejectedAtEveryExtensibleLevel(t *testing.T) {
	fixtures := []string{
		`{"version":2,"version":2,"doll_id":"d","sections":[]}`,
		`{"version":2,"doll_id":"d","sections":[{"id":"x","id":"x"}]}`,
		`{"version":2,"doll_id":"d","sections":[{"id":"x","entries":[{"path":"x","path":"y"}]}]}`,
		`{"version":2,"doll_id":"d","future":{"nested":1,"nested":2},"sections":[]}`,
	}
	for _, fixture := range fixtures {
		if _, err := ParseContractIndex([]byte(fixture), testBounds); !errors.Is(err, ErrInvalidContractIndex) {
			t.Fatalf("duplicate key accepted: %s: %v", fixture, err)
		}
	}
}

func TestUnpreservableUnknownFieldsFailExplicitly(t *testing.T) {
	index := v2IndexFixture()
	index.UnknownFields = UnknownFields{"version": json.RawMessage(`3`)}
	if err := index.Validate(testBounds); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("known-field collision accepted: %v", err)
	}
	index = v2IndexFixture()
	index.Sections[0].Entries[0].UnknownFields = UnknownFields{"future": json.RawMessage(`{"duplicate":1,"duplicate":2}`)}
	if _, err := json.Marshal(index); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("ambiguous unknown value accepted: %v", err)
	}
}

func hasIssue(report CompatibilityReport, code, sectionID string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code && (sectionID == "" || issue.SectionID == sectionID) {
			return true
		}
	}
	return false
}

func rawJSONEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func TestVersion1CompatibilityTreatsAuthorityAsUnknown(t *testing.T) {
	parsed, err := ParseContractIndex([]byte(`{"version":1}`), testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != 1 || parsed.Report.Status != CompatibilityDegraded {
		t.Fatalf("report=%+v", parsed.Report)
	}
	found := false
	for _, issue := range parsed.Report.Issues {
		if issue.Code == "authority_unrepresented" {
			found = true
		}
	}
	if !found {
		t.Fatal("v1 missing authority was silently interpreted")
	}
}

func TestUnknownSectionsProduceExplicitCompatibilityReport(t *testing.T) {
	index := v2IndexFixture()
	index.Sections = append(index.Sections, SectionIndex{ID: "future", Kind: "future_state", SchemaVersion: 1, Entries: []ArchiveEntry{contractEntry("extensions/future/data", []byte("x"))}})
	data, _ := json.Marshal(index)
	parsed, err := ParseContractIndex(data, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Report.Status != CompatibilityCompatible || len(parsed.Report.Issues) == 0 {
		t.Fatalf("optional report=%+v", parsed.Report)
	}
	index.Sections[len(index.Sections)-1].Required = true
	data, _ = json.Marshal(index)
	parsed, err = ParseContractIndex(data, testBounds)
	if err != nil || parsed.Report.Status != CompatibilityIncompatible {
		t.Fatalf("required report=%+v err=%v", parsed.Report, err)
	}
}

func TestIndexRejectsTraversalDuplicatesAndBounds(t *testing.T) {
	index := v2IndexFixture()
	index.Sections[0].Entries[0].Path = "../identity.json"
	if err := index.Validate(testBounds); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("traversal: %v", err)
	}
	index = v2IndexFixture()
	index.Sections[1].Entries[0].Path = index.Sections[0].Entries[0].Path
	if err := index.Validate(testBounds); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("duplicate path: %v", err)
	}
	index = v2IndexFixture()
	if err := index.Validate(ImportBounds{MaxSections: 1, MaxEntries: 1, MaxDeclaredBytes: 1}); !errors.Is(err, ErrInvalidContractIndex) {
		t.Fatalf("bounds: %v", err)
	}
}

func TestAuthorityAndCredentialsRequireExplicitCompatibilityOutcomes(t *testing.T) {
	unverified := AuthorityImportResults([]string{"decision-1", "revocation-1", "consumption-1"}, false)
	for _, result := range unverified {
		if result.Disposition != ImportRequiresVerification {
			t.Fatalf("authority activated: %+v", result)
		}
	}
	credential := CredentialImportResult("smtp-token", false)
	if credential.Disposition != ImportRequiresSetup {
		t.Fatalf("credential discarded or activated: %+v", credential)
	}
}

func TestMemoryTransformationAndRejectionRemainExplicit(t *testing.T) {
	report := ImportCompatibilityReport{Memory: []memory.ImportOutcome{
		{Source: memory.Reference{ID: "image", Revision: 1}, Retained: &memory.Reference{ID: "description", Revision: 1}, Disposition: memory.ImportTransformed, Reason: "image retained as text description"},
		{Source: memory.Reference{ID: "unsupported", Revision: 1}, Disposition: memory.ImportRejected, Reason: "unsupported MIME type"},
	}}
	if len(report.Memory) != 2 || report.Memory[0].Disposition != memory.ImportTransformed || report.Memory[0].Retained == nil || report.Memory[1].Disposition != memory.ImportRejected || report.Memory[1].Reason == "" {
		t.Fatalf("memory outcomes lost: %+v", report.Memory)
	}
}
