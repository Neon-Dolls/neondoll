package memory

type Capabilities struct {
	ContractVersion       int
	ContentTypes          []string
	MaxInlineBytes        uint64
	MaxStreamBytes        uint64
	SupportsUpdates       bool
	SupportsConsolidation bool
	Consistency           ConsistencyCapabilities
}

// ConsistencyCapabilities declares optional guarantees. Zero values mean
// unspecified: Core must never infer a stronger guarantee from their absence.
// These describe provider behavior, not requirements imposed by the contract.
type ConsistencyCapabilities struct {
	// ReplayScope is the identity domain within which a non-empty replay key is
	// checked. ReplayDurable says that check survives provider restart while
	// its retained replay metadata remains available.
	ReplayScope   ReplayScope
	ReplayDurable bool
	// RevisionConflicts reports whether conflicts are advisory/absent,
	// serialized for one supported writer, or atomically enforced by storage.
	RevisionConflicts RevisionConflictGuarantee
	// ReadAfterWrite describes visibility through Recall, not merely direct
	// access to provider-native source bytes.
	ReadAfterWrite ReadAfterWriteConsistency
	// ImportAtomicity describes the rollback boundary. Per-record imports can
	// retain earlier outcomes when a later record fails.
	ImportAtomicity ImportAtomicity
	// RetrievalSemantics distinguishes native semantic/approximate retrieval
	// from exact matching. Ordering is a separate promise.
	RetrievalSemantics    RetrievalSemantics
	DeterministicOrdering bool
}

type ReplayScope string

const (
	ReplayScopeNone   ReplayScope = "none"
	ReplayScopeRecord ReplayScope = "record"
	ReplayScopeDoll   ReplayScope = "doll"
)

type RevisionConflictGuarantee string

const (
	RevisionConflictsNone         RevisionConflictGuarantee = "none"
	RevisionConflictsSingleWriter RevisionConflictGuarantee = "single_writer"
	RevisionConflictsAtomic       RevisionConflictGuarantee = "atomic"
)

type ReadAfterWriteConsistency string

const (
	ReadAfterWriteEventual  ReadAfterWriteConsistency = "eventual"
	ReadAfterWriteImmediate ReadAfterWriteConsistency = "immediate"
)

type ImportAtomicity string

const (
	ImportAtomicityNone       ImportAtomicity = "none"
	ImportAtomicityPerRecord  ImportAtomicity = "per_record"
	ImportAtomicityAllRecords ImportAtomicity = "all_records"
)

type RetrievalSemantics string

const (
	RetrievalProviderNative RetrievalSemantics = "provider_native"
	RetrievalExact          RetrievalSemantics = "exact"
)
