package pulse

// fakeRNG is a deterministic RNG for testing. It returns pre-set samples
// in order and panics if no more samples are available.
type fakeRNG struct {
	samples []float64
	index   int
}

func newFakeRNG(samples ...float64) *fakeRNG {
	return &fakeRNG{samples: samples}
}

func (f *fakeRNG) Float64() float64 {
	if f.index >= len(f.samples) {
		panic("fakeRNG: no more samples")
	}
	v := f.samples[f.index]
	f.index++
	return v
}

// reset resets the sample index to 0.
func (f *fakeRNG) reset() {
	f.index = 0
}
