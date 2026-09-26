package pulse

import (
	"crypto/rand"
	"encoding/binary"
	mathrand "math/rand/v2"
)

// NewProductionRNG creates a new cryptographically seeded RNG source.
// It wraps math/rand/v2's PCG generator seeded with entropy from crypto/rand.
// This is the smallest Go-native production RNG satisfying the RNG interface.
func NewProductionRNG() RNG {
	var seed1, seed2 uint64
	if err := binary.Read(rand.Reader, binary.LittleEndian, &seed1); err != nil {
		// Fallback: use math/rand's automatic seed (crypto/rand failure is
		// catastrophic, but provide a degraded fallback rather than panic).
		seed1 = mathrand.Uint64()
		seed2 = mathrand.Uint64()
	} else {
		binary.Read(rand.Reader, binary.LittleEndian, &seed2)
	}
	return &pcgRNG{src: mathrand.New(mathrand.NewPCG(seed1, seed2))}
}

type pcgRNG struct {
	src *mathrand.Rand
}

func (p *pcgRNG) Float64() float64 {
	return p.src.Float64()
}
