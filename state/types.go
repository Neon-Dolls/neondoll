package state

// Version of the Doll State model.
const CurrentStateVersion = 1

// DollState is the top-level portable state container.
type DollState struct {
	Version      int            `json:"version"`
	Identity     Identity       `json:"identity,omitempty"`
	Soul         Soul           `json:"soul,omitempty"`
	Owner        Owner          `json:"owner,omitempty"`
	CoreConfig   *CoreConfig    `json:"core_config,omitempty"`
	Secrets      Secrets        `json:"secrets,omitempty"`
	ExtensionData map[string]any `json:"extensions,omitempty"`
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

// CoreConfig — installation-specific configuration (NOT portable)
type CoreConfig struct {
	CoreName string                 `json:"core_name"`
	Version  int                    `json:"version"`
	Settings map[string]any         `json:"settings,omitempty"`
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