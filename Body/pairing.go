package body

import "github.com/Neon-Dolls/neondoll/DollNetwork"

// BuildPairingBody constructs the canonical body object from a persisted
// identity state. It is the M1 deliverable: pairing-request data that Core
// can consume, produced without any Core-internal import.
func BuildPairingBody(st *IdentityState) dollnetwork.PairRequestBody {
	body := dollnetwork.PairRequestBody{
		BodyID: string(st.Identity.BodyID),
		Name:   st.Identity.Name,
	}
	if m := &st.Identity.Meta; m != nil {
		body.Implementation = m.Implementation
		body.Platform = m.Platform
		body.Arch = m.Arch
	}
	return body
}

// BuildPairingNetwork builds the network object containing only the WG public
// key (base64). The private key is intentionally absent.
func BuildPairingNetwork(kp *WgKeypair) dollnetwork.PairingNetwork {
	return dollnetwork.PairingNetwork{
		WireGuardPublicKey: kp.PublicKeyBase64(),
	}
}

// BuildPairRequest composes a full canonical pairing request from an
// invitation's id/secret plus the Body's identity and WG public key.
func BuildPairRequest(invitationID, secret string, st *IdentityState, kp *WgKeypair) dollnetwork.PairRequest {
	return dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: invitationID,
		Secret:       secret,
		Body:         BuildPairingBody(st),
		Network:      BuildPairingNetwork(kp),
	}
}
