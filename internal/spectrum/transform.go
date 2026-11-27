// Vectorized real-input STFT path via github.com/tphakala/simd (AVX+FMA / NEON),
// with scalar radix-2 fallback in fft.go when SIMD is unavailable.

package spectrum

import (
	"math"
	"runtime"

	"github.com/tphakala/simd/cpu"
	"github.com/tphakala/simd/f32"
)

// Transform is the FFT interface implemented by [Analyzer] and simdAnalyzer.
type Transform interface {
	// Magnitudes transforms the first Size samples into out, zero-padding short inputs.
	Magnitudes(samples []float32, out []float64) int
	// Bins returns the number of usable magnitude bins (Size()/2).
	Bins() int
	// Size returns the frame length the transform accepts.
	Size() int
}

// NewTransform returns a SIMD-accelerated [Transform] when supported by the CPU,
// or [newAnalyzer] otherwise. size must be a positive power of two.
func NewTransform(size int) (Transform, error) {
	if useSIMD() {
		return newSIMDAnalyzer(size)
	}

	return newAnalyzer(size)
}

// useSIMD reports whether AVX or NEON is available; on other CPUs the scalar radix-2 path is faster.
func useSIMD() bool {
	switch runtime.GOARCH {
	case "amd64":
		return cpu.HasAVX()
	case "arm64":
		return cpu.HasNEON()
	default:
		return false
	}
}

// simdAnalyzer is the SIMD-backed [Transform]. It is not safe for concurrent use.
type simdAnalyzer struct {
	size   int
	plan   *f32.STFTPlan
	window []float32
	spec   []complex64
}

func newSIMDAnalyzer(size int) (*simdAnalyzer, error) {
	if size < 2 || size&(size-1) != 0 {
		return nil, ErrFFTSize
	}
	plan, err := f32.NewSTFTPlan(size)
	if err != nil {
		return nil, err
	}
	a := &simdAnalyzer{
		size:   size,
		plan:   plan,
		window: make([]float32, size),
		spec:   make([]complex64, size/2+1),
	}
	for i := range a.window {
		// Periodic Hann window matching Analyzer.
		a.window[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(size)))
	}

	return a, nil
}

// NewAnalyzer returns an [Analyzer] for size-point frames (must be a positive power of two).
func newAnalyzer(size int) (*Analyzer, error) {
	if size < 2 || size&(size-1) != 0 {
		return nil, ErrFFTSize
	}

	a := &Analyzer{
		size:   size,
		window: make([]float64, size),
		re:     make([]float64, size),
		im:     make([]float64, size),
		cos:    make([]float64, size/2),
		sin:    make([]float64, size/2),
		rev:    make([]int, size),
	}
	for i := range size {
		// Periodic Hann window.
		a.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(size))
	}
	for i := range size {
		a.rev[i] = reverseBits(i, size)
	}
	for i := range size / 2 {
		angle := -2 * math.Pi * float64(i) / float64(size)
		a.cos[i] = math.Cos(angle)
		a.sin[i] = math.Sin(angle)
	}

	return a, nil
}

func (a *simdAnalyzer) Size() int { return a.size }

func (a *simdAnalyzer) Bins() int { return a.size / 2 }

// Magnitudes matches [Analyzer.Magnitudes] using the SIMD RFFT plan.
func (a *simdAnalyzer) Magnitudes(samples []float32, out []float64) int {
	// RFFT applies the window, zero-pads short inputs, and writes the Hermitian half-spectrum.
	a.plan.RFFT(a.spec, samples, a.window)

	bins := min(a.Bins(), len(out))
	// Normalize by N and the Hann coherent gain (0.5) to match Analyzer.
	scale := 2 / (float64(a.size) * 0.5)
	for k := range bins {
		out[k] = math.Hypot(float64(real(a.spec[k])), float64(imag(a.spec[k]))) * scale
	}

	return bins
}
