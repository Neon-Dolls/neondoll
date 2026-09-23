package dollcard

// cardHeader is the format URI metadata stored in card.json.
type cardHeader struct {
	Version int `json:"version"`
}

// Supported DollCard format version.
const currentCardVersion = 1
