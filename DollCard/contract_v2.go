package dollcard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Memory"
	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

const ContractVersion2 = 2

var ErrInvalidContractIndex = errors.New("dollcard: invalid contract index")

type SectionKind string

const (
	SectionIdentity    SectionKind = "identity"
	SectionSoul        SectionKind = "soul"
	SectionSelf        SectionKind = "self"
	SectionAvatar      SectionKind = "avatar"
	SectionPreferences SectionKind = "preferences"
	SectionSecrets     SectionKind = "secrets"
	SectionSkills      SectionKind = "skills"
	SectionMemories    SectionKind = "memories"
	SectionPeers       SectionKind = "peers"
	SectionAuthority   SectionKind = "authority"
	SectionDrives      SectionKind = "drives"
	SectionGoals       SectionKind = "goals"
	SectionIntentions  SectionKind = "intentions"
	SectionExtensions  SectionKind = "extensions"
	SectionLegacyOwner SectionKind = "legacy_owner"
	SectionLegacyAuth  SectionKind = "legacy_auth"
)

type ArchiveEntry struct {
	Path          string        `json:"path"`
	SHA256        string        `json:"sha256"`
	Size          uint64        `json:"size"`
	UnknownFields UnknownFields `json:"-"`
}

func (e *ArchiveEntry) UnmarshalJSON(data []byte) error {
	type knownEntry ArchiveEntry
	var known knownEntry
	unknown, err := decodeExtensibleObject(data, &known, "path", "sha256", "size")
	if err != nil {
		return err
	}
	*e = ArchiveEntry(known)
	e.UnknownFields = unknown
	return nil
}

func (e ArchiveEntry) MarshalJSON() ([]byte, error) {
	type knownEntry ArchiveEntry
	known := knownEntry(e)
	known.UnknownFields = nil
	return marshalExtensibleObject(known, e.UnknownFields, "path", "sha256", "size")
}

type SectionIndex struct {
	ID            string      `json:"id"`
	Kind          SectionKind `json:"kind"`
	SchemaVersion int         `json:"schema_version"`
	Required      bool        `json:"required"`
	// Empty explicitly represents a portable area with no retained content.
	// A present empty section is distinct from an omitted section.
	Empty         bool           `json:"empty"`
	Entries       []ArchiveEntry `json:"entries,omitempty"`
	Compatibility []string       `json:"compatibility,omitempty"`
	UnknownFields UnknownFields  `json:"-"`
}

func (s *SectionIndex) UnmarshalJSON(data []byte) error {
	type knownSection SectionIndex
	var known knownSection
	unknown, err := decodeExtensibleObject(data, &known, "id", "kind", "schema_version", "required", "empty", "entries", "compatibility")
	if err != nil {
		return err
	}
	*s = SectionIndex(known)
	s.UnknownFields = unknown
	return nil
}

func (s SectionIndex) MarshalJSON() ([]byte, error) {
	type knownSection SectionIndex
	known := knownSection(s)
	known.UnknownFields = nil
	return marshalExtensibleObject(known, s.UnknownFields, "id", "kind", "schema_version", "required", "empty", "entries", "compatibility")
}

// IndexV2 is the next DollCard card.json contract. Production v1 encoding and
// decoding intentionally remain unchanged in this milestone.
type IndexV2 struct {
	Version       int            `json:"version"`
	DollID        string         `json:"doll_id"`
	Sections      []SectionIndex `json:"sections"`
	UnknownFields UnknownFields  `json:"-"`
}

func (i *IndexV2) UnmarshalJSON(data []byte) error {
	type knownIndex IndexV2
	var known knownIndex
	unknown, err := decodeExtensibleObject(data, &known, "version", "doll_id", "sections")
	if err != nil {
		return err
	}
	*i = IndexV2(known)
	i.UnknownFields = unknown
	return nil
}

func (i IndexV2) MarshalJSON() ([]byte, error) {
	type knownIndex IndexV2
	known := knownIndex(i)
	known.UnknownFields = nil
	return marshalExtensibleObject(known, i.UnknownFields, "version", "doll_id", "sections")
}

type ImportBounds struct {
	MaxSections      uint64
	MaxEntries       uint64
	MaxDeclaredBytes uint64
}

func (i IndexV2) Validate(bounds ImportBounds) error {
	if i.Version != ContractVersion2 || strings.TrimSpace(i.DollID) == "" || bounds.MaxSections == 0 || bounds.MaxEntries == 0 || bounds.MaxDeclaredBytes == 0 || uint64(len(i.Sections)) > bounds.MaxSections {
		return ErrInvalidContractIndex
	}
	if validateUnknownFields(i.UnknownFields, "version", "doll_id", "sections") != nil {
		return ErrInvalidContractIndex
	}
	sections, paths, kinds := map[string]bool{}, map[string]bool{}, map[SectionKind]bool{}
	var entries, size uint64
	for _, section := range i.Sections {
		if strings.TrimSpace(section.ID) == "" || strings.TrimSpace(string(section.Kind)) == "" || sections[section.ID] || section.SchemaVersion <= 0 {
			return ErrInvalidContractIndex
		}
		if section.Empty == (len(section.Entries) > 0) {
			return ErrInvalidContractIndex
		}
		if validateUnknownFields(section.UnknownFields, "id", "kind", "schema_version", "required", "empty", "entries", "compatibility") != nil {
			return ErrInvalidContractIndex
		}
		sections[section.ID] = true
		if _, known := knownSectionVersions[section.Kind]; known && kinds[section.Kind] {
			return ErrInvalidContractIndex
		}
		if _, known := knownSectionVersions[section.Kind]; known && !section.Required {
			return ErrInvalidContractIndex
		}
		kinds[section.Kind] = true
		for _, requirement := range section.Compatibility {
			if strings.TrimSpace(requirement) == "" {
				return ErrInvalidContractIndex
			}
		}
		for _, entry := range section.Entries {
			if validateUnknownFields(entry.UnknownFields, "path", "sha256", "size") != nil {
				return ErrInvalidContractIndex
			}
			if !skill.SafeRelativePath(entry.Path) || paths[entry.Path] || len(entry.SHA256) != 64 {
				return ErrInvalidContractIndex
			}
			if _, err := hex.DecodeString(entry.SHA256); err != nil {
				return ErrInvalidContractIndex
			}
			paths[entry.Path] = true
			entries++
			if entry.Size > bounds.MaxDeclaredBytes-size {
				return ErrInvalidContractIndex
			}
			size += entry.Size
			if entries > bounds.MaxEntries {
				return ErrInvalidContractIndex
			}
		}
	}
	return nil
}

func ValidateEntryData(entry ArchiveEntry, data []byte) error {
	if uint64(len(data)) != entry.Size {
		return ErrInvalidContractIndex
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(entry.SHA256, hex.EncodeToString(digest[:])) {
		return ErrInvalidContractIndex
	}
	return nil
}

// PreserveIndexedPayloads validates and copies every payload referenced by the
// index. It is the contract-level pass-through primitive used by a future v2
// archive pipeline; returned bytes do not alias the caller's buffers.
func PreserveIndexedPayloads(index IndexV2, payloads map[string][]byte) (map[string][]byte, error) {
	preserved := make(map[string][]byte)
	for _, section := range index.Sections {
		for _, entry := range section.Entries {
			payload, ok := payloads[entry.Path]
			if !ok || ValidateEntryData(entry, payload) != nil {
				return nil, ErrInvalidContractIndex
			}
			preserved[entry.Path] = append([]byte(nil), payload...)
		}
	}
	return preserved, nil
}

type CompatibilityStatus string

const (
	CompatibilityCompatible   CompatibilityStatus = "compatible"
	CompatibilityDegraded     CompatibilityStatus = "degraded"
	CompatibilityIncompatible CompatibilityStatus = "incompatible"
)

type CompatibilityIssue struct {
	Code      string `json:"code"`
	SectionID string `json:"section_id,omitempty"`
	Message   string `json:"message"`
}
type CompatibilityReport struct {
	Status CompatibilityStatus  `json:"status"`
	Issues []CompatibilityIssue `json:"issues,omitempty"`
}
type ParsedIndex struct {
	Version int
	V2      *IndexV2
	Report  CompatibilityReport
}

var knownSectionVersions = map[SectionKind]int{
	SectionIdentity: 1, SectionSoul: 1, SectionSelf: 1, SectionAvatar: 1,
	SectionPreferences: 1, SectionSecrets: 1, SectionSkills: skill.SchemaVersion,
	SectionMemories: memory.SchemaVersion, SectionPeers: 1, SectionAuthority: 1,
	SectionDrives: 1, SectionGoals: 1, SectionIntentions: 1, SectionExtensions: 1,
	SectionLegacyOwner: 1, SectionLegacyAuth: 1,
}

var requiredV2Sections = []SectionKind{
	SectionIdentity, SectionSoul, SectionSelf, SectionAvatar, SectionPreferences,
	SectionSecrets, SectionSkills, SectionMemories, SectionPeers, SectionAuthority,
	SectionDrives, SectionGoals, SectionIntentions, SectionExtensions,
	SectionLegacyOwner, SectionLegacyAuth,
}

func ParseContractIndex(data []byte, bounds ImportBounds) (ParsedIndex, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ParsedIndex{}, ErrInvalidContractIndex
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(data, &header); err != nil {
		return ParsedIndex{}, ErrInvalidContractIndex
	}
	var version int
	if err := json.Unmarshal(header["version"], &version); err != nil {
		return ParsedIndex{}, ErrInvalidContractIndex
	}
	if version == 1 {
		return ParsedIndex{Version: 1, Report: CompatibilityReport{Status: CompatibilityDegraded, Issues: []CompatibilityIssue{
			{Code: "legacy_v1", Message: "DollCard v1 remains readable by the existing importer"},
			{Code: "authority_unrepresented", SectionID: "authority", Message: "missing authority is unknown, not allow or prohibit"},
			{Code: "skills_legacy", SectionID: "skills", Message: "v1 has no complete indexed Skill catalog guarantee"},
		}}}, nil
	}
	if version != ContractVersion2 {
		return ParsedIndex{}, ErrInvalidContractIndex
	}
	var index IndexV2
	if err := json.Unmarshal(data, &index); err != nil || index.Validate(bounds) != nil {
		return ParsedIndex{}, ErrInvalidContractIndex
	}
	report := assessCompatibility(index)
	return ParsedIndex{Version: version, V2: &index, Report: report}, nil
}

func assessCompatibility(index IndexV2) CompatibilityReport {
	report := CompatibilityReport{Status: CompatibilityCompatible}
	seenKinds := map[SectionKind]bool{}
	for _, section := range index.Sections {
		supported, known := knownSectionVersions[section.Kind]
		if !known {
			code := "unknown_optional_section"
			if section.Required {
				code, report.Status = "unknown_required_section", CompatibilityIncompatible
			}
			report.Issues = append(report.Issues, CompatibilityIssue{Code: code, SectionID: section.ID, Message: "section semantics are not understood; index metadata and referenced payloads remain preservable"})
			continue
		}
		seenKinds[section.Kind] = true
		if section.SchemaVersion != supported {
			report.Status = CompatibilityIncompatible
			report.Issues = append(report.Issues, CompatibilityIssue{Code: "unsupported_section_version", SectionID: section.ID, Message: fmt.Sprintf("schema %d is unsupported", section.SchemaVersion)})
		}
	}
	for _, kind := range requiredV2Sections {
		if !seenKinds[kind] {
			report.Status = CompatibilityIncompatible
			report.Issues = append(report.Issues, CompatibilityIssue{Code: "missing_section", SectionID: string(kind), Message: "v2 index must explicitly represent this portable area"})
		}
	}
	return report
}

type ImportDisposition string

const (
	ImportAccepted             ImportDisposition = "accepted"
	ImportDormant              ImportDisposition = "dormant"
	ImportPreservedUnknown     ImportDisposition = "preserved_unknown"
	ImportTransformed          ImportDisposition = "transformed"
	ImportRejected             ImportDisposition = "rejected"
	ImportRequiresVerification ImportDisposition = "requires_verification"
	ImportRequiresSetup        ImportDisposition = "requires_setup"
)

type ImportItemResult struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Disposition ImportDisposition `json:"disposition"`
	Reason      string            `json:"reason,omitempty"`
}
type ImportCompatibilityReport struct {
	Index  CompatibilityReport    `json:"index"`
	Items  []ImportItemResult     `json:"items,omitempty"`
	Memory []memory.ImportOutcome `json:"memory,omitempty"`
}

func AuthorityImportResults(decisionIDs []string, verified bool) []ImportItemResult {
	disposition, reason := ImportAccepted, ""
	if !verified {
		disposition, reason = ImportRequiresVerification, "authority provenance is unverified and must not be activated"
	}
	results := make([]ImportItemResult, 0, len(decisionIDs))
	for _, id := range decisionIDs {
		results = append(results, ImportItemResult{Kind: "authority", ID: id, Disposition: disposition, Reason: reason})
	}
	return results
}

func CredentialImportResult(id string, usable bool) ImportItemResult {
	if usable {
		return ImportItemResult{Kind: "credential", ID: id, Disposition: ImportAccepted}
	}
	return ImportItemResult{Kind: "credential", ID: id, Disposition: ImportRequiresSetup, Reason: "credential is retained but unavailable in this installation"}
}
