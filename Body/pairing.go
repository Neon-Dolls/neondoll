package body

import "github.com/Neon-Dolls/neondoll/DollNetwork"

// BuildPairingBody constructs the current pairing body object from a persisted
// identity state. It is the M1 deliverable: pairing-request data that Core
// can consume, produced without any Core-internal import. The exact wire shape
// is finalized in M2; we carry the M1 field set today.
func BuildPairingBody(st *IdentityState) dollnetwork.PairRequestBody {
	return dollnetwork.PairRequestBody{
		BodyID:         string(st.Identity.BodyID),
		Name:           st.Identity.Name,
		Implementation: st.Identity.Meta.Implementation,
		Platform:       st.Identity.Meta.Platform,
		Arch:           st.Identity.Meta.Arch,
	}
}

// BuildPairingNetwork builds the network object containing only the WG public
// key (base64). The private key is intentionally absent.
func BuildPairingNetwork(kp *WgKeypair) dollnetwork.PairingNetwork {
	return dollnetwork.PairingNetwork{
		WireGuardPublicKey: kp.PublicKeyBase64(),
	}
}

// BuildPairRequest composes a pairing request from an invitation's id/secret
// plus the Body's identity and WG public key. The M1 field set is carried here
// as a Doll Network concept; exact wire details are finalized in M2.
func BuildPairRequest(invitationID, secret string, st *IdentityState, kp *WgKeypair) dollnetwork.PairRequest {
	return dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: invitationID,
		Secret:       secret,
		Body:         BuildPairingBody(st),
		Network:      BuildPairingNetwork(kp),
	}
}
