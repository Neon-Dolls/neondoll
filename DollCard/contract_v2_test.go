package dollcard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestUnknownIndexFieldsAreExplicitlyNotPreserved(t *testing.T) {
	data, err := json.Marshal(v2IndexFixture())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["future_top_level"] = map[string]any{"value": "retained by a future parser"}
	sections := raw["sections"].([]any)
	sections[0].(map[string]any)["future_section_field"] = []any{"future", "metadata"}
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseContractIndex(data, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Report.Status != CompatibilityDegraded || !hasIssue(parsed.Report, "unknown_index_field_not_preserved", "") || !hasIssue(parsed.Report, "unknown_section_field_not_preserved", "identity") {
		t.Fatalf("unknown fields not reported accurately: %+v", parsed.Report)
	}
	roundTrip, err := json.Marshal(parsed.V2)
	if err != nil {
		t.Fatal(err)
	}
	if stringContains(string(roundTrip), "future_top_level") || stringContains(string(roundTrip), "future_section_field") {
		t.Fatalf("test expected current parser not to retain unknown fields: %s", roundTrip)
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

func stringContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
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
	if parsed.Report.Status != CompatibilityDegraded || len(parsed.Report.Issues) == 0 {
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
