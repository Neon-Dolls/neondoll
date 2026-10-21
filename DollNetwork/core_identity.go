package dollnetwork

const (
	CoreIdentityVersion   = 1
	CoreIdentityAlgorithm = "Ed25519"
)

// CoreIdentityResponse is the public, read-only bootstrap identity document.
// It is discovery material for the pairing ceremony, never proof that a
// caller is an enrolled Body.
type CoreIdentityResponse struct {
	Version   int    `json:"version"`
	CoreID    string `json:"core_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}
