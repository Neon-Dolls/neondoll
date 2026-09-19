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

// MemoryItem is a single memory record.
type MemoryItem struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Level     int    `json:"level"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Drives — the Doll's active drives and motivations.
type Drives struct {
	Items []DriveItem `json:"items,omitempty"`
}

// DriveItem represents a single active drive.
type DriveItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Priority int   `json:"priority"`
}

// Goals — the Doll's tracked objectives.
type Goals struct {
	Items []GoalItem `json:"items,omitempty"`
}

// GoalItem represents a single goal.
type GoalItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Completed bool   `json:"completed"`
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