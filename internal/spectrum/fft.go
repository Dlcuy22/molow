// Scalar iterative radix-2 FFT for display analysis.

package spectrum

import (
	"errors"
	"math"
)

var ErrFFTSize = errors.New("fft: size must be a positive power of two")

// Analyzer transforms fixed-size real frames using preallocated scratch buffers.
type Analyzer struct {
	size   int
	window []float64 // periodic Hann window

	re, im []float64

	cos, sin []float64 // precomputed twiddle factors
	rev      []int
}

// Size returns the frame length the [Analyzer] accepts.
func (a *Analyzer) Size() int { return a.size }

// Bins returns the number of usable magnitude bins (size/2).
func (a *Analyzer) Bins() int { return a.size / 2 }

// Magnitudes transforms the first Size samples into out, low frequency first, returning bins written.
// Short inputs are windowed and zero-padded.
func (a *Analyzer) Magnitudes(samples []float32, out []float64) int {
	n := a.size
	for i := range n {
		var s float64
		if i < len(samples) {
			s = float64(samples[i]) * a.window[i]
		}
		a.re[i] = s
		a.im[i] = 0
	}

	a.transform()

	bins := min(a.Bins(), len(out))
	for k := range bins {
		a.re[k] = math.Hypot(a.re[k], a.im[k])
	}
	// Normalize by N and the Hann coherent gain (0.5) so levels stay consistent across FFT sizes.
	scale := 2 / (float64(n) * 0.5)
	for k := range bins {
		out[k] = a.re[k] * scale
	}

	return bins
}

// transform runs an in-place iterative radix-2 Cooley-Tukey FFT over re and im.
func (a *Analyzer) transform() {
	n := a.size
	for i := range n {
		if j := a.rev[i]; i < j {
			a.re[i], a.re[j] = a.re[j], a.re[i]
			a.im[i], a.im[j] = a.im[j], a.im[i]
		}
	}

	for length := 2; length <= n; length <<= 1 {
		half := length / 2
		step := n / length
		for start := 0; start < n; start += length {
			for k := range half {
				tw := k * step
				wr, wi := a.cos[tw], a.sin[tw]
				even := start + k
				odd := even + half
				ur, ui := a.re[even], a.im[even]
				vr := a.re[odd]*wr - a.im[odd]*wi
				vi := a.re[odd]*wi + a.im[odd]*wr
				a.re[even] = ur + vr
				a.im[even] = ui + vi
				a.re[odd] = ur - vr
				a.im[odd] = ui - vi
			}
		}
	}
}

// reverseBits reverses the low log2(n) bits of i for the bit-reversal permutation.
func reverseBits(i, n int) int {
	var out int
	for k := 1; k < n; k <<= 1 {
		out <<= 1
		if i&k != 0 {
			out |= 1
		}
	}

	return out
}
