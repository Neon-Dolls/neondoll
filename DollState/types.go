package dollstate

// Version of the Doll State model.
const CurrentStateVersion = 1

// DollState is the top-level portable state container.
type DollState struct {
	Version      int              `json:"version"`
	Identity     Identity         `json:"identity,omitempty"`
	Soul         Soul             `json:"soul,omitempty"`
	Owner        Owner            `json:"owner,omitempty"`
	Self         Self             `json:"self,omitempty"`
	Memories     Memories         `json:"memories,omitempty"`
	Drives       Drives           `json:"drives,omitempty"`
	Goals        Goals            `json:"goals,omitempty"`
	Skills       Skills           `json:"skills,omitempty"`
	Intentions   Intentions       `json:"intentions,omitempty"`
	Auth         Auth             `json:"auth,omitempty"`
	Secrets      Secrets          `json:"secrets,omitempty"`
	ExtensionData map[string]any  `json:"extensions,omitempty"`
}

// Identity — which Doll is this?
type Identity struct {
	DollID       string `json:"doll_id"`
	CanonicalName string `json:"canonical_name"`
	TemplateRef  string `json:"template_ref,omitempty"`
}

// Soul — who the Doll fundamentally is (prose)
type Soul struct {
	Revision int    `json:"revision"`
	Content  string `json:"content"`
}

// Owner — the Doll's distinguished owner
type Owner struct {
	OwnerID string `json:"owner_id"`
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
}

// Self — the Doll's self-concept and identity markers.
type Self struct {
	DisplayName string `json:"display_name,omitempty"`
	Pronouns    string `json:"pronouns,omitempty"`
	Tagline     string `json:"tagline,omitempty"`
}

// Memories — episodic and semantic memory storage.
type Memories struct {
	Items []MemoryItem `json:"items,omitempty"`
}

// Kind constants for MemoryItem.
const (
	KindHumanMessage = "human_message"
	KindDollResponse = "doll_response"
)

// MemoryItem is a single memory record in the Doll's portable memory stream.
//
// InteractionID groups records belonging to the same experienced interaction.
// It is NOT a Session, Conversation, Thread, or larger abstraction — it exists
// solely to associate the human message with the doll response that followed.
//
// Sequence provides deterministic global ordering across the entire Doll
// memory stream. It does NOT reset per interaction. Every new record in the
// stream receives a monotonically increasing Sequence value regardless of
// which interaction it belongs to.
//
//   Sequence orders experience. InteractionID groups related experience.
//
// Do not rely on SQLite row IDs, Timestamp alone, or Doll Card JSONL line
// position as the semantic ordering contract. Timestamp records wall-clock
// time; Sequence defines logical global order. InteractionID provides
// associative grouping independent of ordering.
//
// Level was present in an earlier version but lacked any defined semantic
// meaning: no doc comment, no constants, no mention in the Doll Card spec
// or any concept document. It was genuinely undefined and has been removed.
type MemoryItem struct {
	ID            string `json:"id"`
	InteractionID string `json:"interaction_id,omitempty"`
	Kind          string `json:"kind"`
	Content       string `json:"content"`
	Sequence      int    `json:"sequence"`
	Timestamp     string `json:"timestamp,omitempty"`
}

// Drives — the Doll's durable motivations and directions.
type Drives struct {
	Items []DriveItem `json:"items,omitempty"`
}

// DriveItem represents a single durable motivation or direction.
//
// Name provides the core human/cognition-readable content. Description may
// carry a richer explanation but is not required when Name alone suffices.
//
// An ID is a portable semantic identifier — never derived from a SQLite row
// ID, runtime handle, or other non-portable source.
type DriveItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Goal state constants.
const (
	GoalStateActive    = "active"
	GoalStateCompleted = "completed"
)

// Goals — the Doll's tracked desired outcomes.
type Goals struct {
	Items []GoalItem `json:"items,omitempty"`
}

// GoalItem represents a single durable desired outcome.
//
// Name provides the core content. Description may carry a richer explanation.
// State marks lifecycle explicitly: "active" means being pursued, "completed"
// means achieved. State is always serialized — active is never inferred from
// a missing boolean.
// DriveID optionally references the semantic identifier of the Drive that
// motivates this Goal. Both IDs are portable semantic identifiers.
type GoalItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	DriveID     string `json:"drive_id,omitempty"`
}

// Skills — learned or installed capabilities.
type Skills struct {
	Items []SkillItem `json:"items,omitempty"`
}

// SkillItem is a reference to an installed Agent Skill.
// The actual canonical Skill remains the Agent Skills directory rooted at SKILL.md.
// Path is a portable relative path within the Skills directory, not an absolute
// host filesystem path. DollCard export preserves the entire referenced directory;
// import may install it at a different physical location.
type SkillItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

// Intention lifecycle state constants.
const (
	IntentionStatePending    = "pending"
	IntentionStateInProgress = "in_progress"
	IntentionStateCompleted  = "completed"
)

// Intentions — Spark's durable future cognitive obligations.
type Intentions struct {
	Items []IntentionItem `json:"items,omitempty"`
}

// IntentionItem represents a single future cognitive obligation.
//
// ID provides stable identity. Subject is what Spark intends to
// reconsider or do cognitively. Description may carry why this
// Intention exists. WakeTime is an RFC3339 UTC timestamp
// representing when this cognition becomes due.
//
// State marks lifecycle explicitly. For Core 1, "pending" is the only
// required state — it means the Intention exists and has not yet been
// fulfilled.
//
// An Intention is NOT a scheduler job, timer handle, goroutine state,
// queue entry, or any other Core-local runtime machinery. It is portable
// semantic Doll State that survives without any particular Core runtime.
type IntentionItem struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	WakeTime    string `json:"wake_time"`
	State       string `json:"state"`
}

// Auth — authentication and authorization state.
type Auth struct {
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Secrets — portable secrets the Doll knows
type Secrets struct {
	Items []SecretItem `json:"items,omitempty"`
}

type SecretItem struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// NewDollState creates a DollState with default version.
func NewDollState() DollState {
	return DollState{
		Version: CurrentStateVersion,
	}
}