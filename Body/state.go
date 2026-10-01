// Package body implements the reusable, Core-independent runtime for
// a NeonDoll reference Body.
package body

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"time"
)

// CoreAddress represents a Core's endpoint in the Doll Network.
type CoreAddress struct {
	// IP is the Core's IPv6 address.
	IP net.IP `json:"ip"`
	// Port is the WireGuard UDP port.
	Port uint16 `json:"port"`
	// PublicKey is the Core's WireGuard public key.
	PublicKey [32]byte `json:"public_key"`
}

// String returns a string representation suitable for logging.
// It omits the public key for brevity.
func (a CoreAddress) String() string {
	return fmt.Sprintf("[%v]:%d", a.IP, a.Port)
}

// MembershipState represents the persisted membership state of a Body.
// It contains the CoreAddresses that the Body has learned about.
type MembershipState struct {
	// CoreAddresses is the list of Core addresses known to this Body.
	// It is populated by the pairing process.
	CoreAddresses []CoreAddress `json:"core_addresses"`
	// LastUpdated is the time when this state was last updated.
	LastUpdated time.Time `json:"last_updated"`
}

// NewMembershipState creates an empty membership state.
func NewMembershipState() *MembershipState {
	return &MembershipState{
		CoreAddresses: []CoreAddress{},
		LastUpdated:   time.Time{},
	}
}

// LoadMembershipState loads the membership state from the given file path.
func LoadMembershipState(path string) (*MembershipState, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state MembershipState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SelectCoreIPv6 returns a valid Core IPv6 address from the membership state.
// It returns the first address that is a valid IPv6 address.
// If no addresses are present, it returns an error.
func (s *MembershipState) SelectCoreIPv6() (net.IP, error) {
	if len(s.CoreAddresses) == 0 {
		return nil, fmt.Errorf("no core addresses available")
	}
	// For simplicity, we take the first one.
	// In a real implementation, we might want to choose based on latency or priority.
	addr := s.CoreAddresses[0]
	if addr.IP == nil || addr.IP.To16() == nil {
		return nil, fmt.Errorf("invalid IPv6 address in core address: %v", addr.IP)
	}
	return addr.IP, nil
}
