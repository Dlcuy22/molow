// Package spectrum turns the player's live audio tap into bar-visualizer band magnitudes.
//
// It is display-only and runs on a caller-owned goroutine, so slow frames drop
// display frames without affecting playback.
package spectrum

import (
	"math"
	"slices"
	"time"
)

// Tap is the read side of the engine's visualizer feed, decoupled from the player
// facade for testing.
type Tap interface {
	// Read copies up to len(dst) mono frames without blocking and returns the count (0 if empty).
	Read(dst []float32) int
}

// Config configures the spectrum transform and display scaling. Unset fields use [DefaultConfig].
type Config struct {
	// Bars is the number of output frequency bands, independent of FFT size.
	Bars int

	// FFT is the transform length (must be a power of two).
	FFT int

	// SampleRate is the tapped audio rate in Hz.
	SampleRate int

	// MinHz and MaxHz bound the analyzed frequency range.
	MinHz float64
	MaxHz float64

	// RangeDB is the dB span below the running peak that maps to zero (unused by [ScaleFixed]).
	RangeDB float64

	// Scale selects vertical mapping: [ScaleDynamic] or [ScaleFixed].
	Scale string

	// Release is the peak follower's per-frame decay in [0, 1) at 60 Hz; [Runner] rescales it to Rate.
	Release float64

	// DelayMs adds user-configured output latency compensation in milliseconds, on top of the jitter cushion.
	DelayMs int

	// Rate is the target frame rate in Hz (0 uses [DefaultConfig]).
	Rate int
}

// ScaleDynamic tracks the recent peak; ScaleFixed pins the window to fixedFloorDB..0 dBFS.
const (
	ScaleDynamic = "dynamic"
	ScaleFixed   = "fixed"
)

const fixedFloorDB = -100

// DefaultConfig returns the default visualizer configuration (150 bars, 20 Hz-20 kHz, 48 kHz, 60 Hz).
func DefaultConfig() Config {
	return Config{
		Bars:       150,
		FFT:        8192,
		SampleRate: 48000,
		MinHz:      20,
		MaxHz:      20000,
		RangeDB:    60,
		Scale:      ScaleDynamic,
		Release:    0.92,
		Rate:       60,
	}
}

// Param describes one visualizer control for UI rendering.
// For "choice" and "steps" kinds, Choices holds the allowed values and Min/Max/Step are zero.
type Param struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Kind    string  `json:"kind"` // "int" | "float" | "choice" | "steps"
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step"`
	Default float64 `json:"default"`

	// Choices lists allowed values in ascending order for "choice" and "steps" kinds.
	Choices []int `json:"choices,omitempty"`
}

// ChoiceFFTSizes are the supported FFT sizes offered by [Schema].
var ChoiceFFTSizes = []int{1024, 2048, 4096, 8192, 16384}

// ChoiceRates are the supported frame rates in Hz offered by [Schema].
var ChoiceRates = []int{30, 60, 120, 180}

// Schema returns the UI control definitions for the visualizer.
func Schema() []Param {
	return []Param{
		{Key: "bars", Label: "Points", Kind: "int", Min: 16, Max: 256, Step: 1, Default: 150},
		{Key: "fft", Label: "Resolution", Kind: "choice", Choices: ChoiceFFTSizes, Default: float64(DefaultConfig().FFT)},
		{Key: "minHz", Label: "Low cut", Kind: "int", Min: 10, Max: 500, Step: 1, Default: 20},
		{Key: "maxHz", Label: "High cut", Kind: "int", Min: 2000, Max: 20000, Step: 100, Default: 20000},
		{Key: "rate", Label: "Frame rate", Kind: "steps", Choices: ChoiceRates, Default: float64(DefaultConfig().Rate)},
	}
}

// Runner computes one spectrum frame per call using preallocated scratch buffers.
type Runner struct {
	cfg Config
	tap Tap
	an  Transform

	window []float32
	filled int

	tmp []float32 // drain buffer for tap reads

	// backlog buffers incoming tap bursts so the window advances smoothly by wall-clock time.
	// len(backlog) is the current display lag in samples.
	backlog []float32

	last time.Time
	now  func() time.Time // injectable clock for tests

	mags    []float64
	bands   []float64
	out     []float64
	smooth  []float64 // scratch for neighbour averaging
	centers []float64 // log-spaced center frequencies per bar

	peak float64 // smoothed recent max in dB for ScaleDynamic

	// emptyTicks counts consecutive frames with no new samples past the cushion;
	// after holdTicks, out drains toward zero.
	emptyTicks int
}

// New validates cfg, applies defaults, and returns a [Runner] reading from tap.
func New(tap Tap, cfg Config) (*Runner, error) {
	cfg = withDefaults(cfg)

	an, err := NewTransform(cfg.FFT)
	if err != nil {
		return nil, err
	}
	// Cap bars to available FFT bins to avoid oversampling.
	if bins := an.Bins(); cfg.Bars > bins {
		cfg.Bars = bins
	}

	r := &Runner{
		cfg:     cfg,
		tap:     tap,
		an:      an,
		window:  make([]float32, cfg.FFT),
		tmp:     make([]float32, cfg.FFT*2),
		backlog: make([]float32, 0, cfg.FFT*3),
		now:     time.Now,
		mags:    make([]float64, an.Bins()),
		bands:   make([]float64, cfg.Bars),
		out:     make([]float64, cfg.Bars),
		smooth:  make([]float64, cfg.Bars),
		centers: barCenters(cfg),
		peak:    math.Inf(-1),
	}

	return r, nil
}

func withDefaults(cfg Config) Config {
	def := DefaultConfig()
	if cfg.Bars <= 0 {
		cfg.Bars = def.Bars
	}
	if !slices.Contains(ChoiceFFTSizes, cfg.FFT) {
		cfg.FFT = def.FFT
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = def.SampleRate
	}
	if cfg.MinHz <= 0 {
		cfg.MinHz = def.MinHz
	}
	if cfg.MaxHz <= 0 {
		cfg.MaxHz = def.MaxHz
	}
	if cfg.RangeDB <= 0 {
		cfg.RangeDB = def.RangeDB
	}
	switch cfg.Scale {
	case ScaleDynamic, ScaleFixed:
	default:
		cfg.Scale = def.Scale
	}
	if cfg.Release <= 0 || cfg.Release >= 1 {
		cfg.Release = def.Release
	}
	// Clamp negative delay; the display cannot lead the tap.
	if cfg.DelayMs < 0 {
		cfg.DelayMs = 0
	}
	if !slices.Contains(ChoiceRates, cfg.Rate) {
		cfg.Rate = def.Rate
	}

	return cfg
}

// Frame dynamics are tuned for 60 Hz and rescaled by the *PerTick methods.
const holdEmptyTicks = 6 // 60 Hz ticks to hold before draining (~100 ms)

const drainFactor = 0.92 // 60 Hz per-tick decay during sustained drought

const peakRise = 0.65 // 60 Hz rise factor toward a new peak

// maxStep caps a single Frame advance so a stalled pump cannot jump through the backlog.
const maxStep = 200 * time.Millisecond

// targetLagDivisor sizes the ~100 ms jitter cushion (SampleRate/divisor) to absorb late bursts.
const targetLagDivisor = 10

// maxLagDivisor caps backlog above the window (SampleRate/divisor, ~250 ms) by dropping oldest samples.
const maxLagDivisor = 4

func (r *Runner) holdTicks() int { return max(1, r.cfg.Rate*holdEmptyTicks/60) }

func (r *Runner) drainPerTick() float64 { return math.Pow(drainFactor, 60/float64(r.cfg.Rate)) }

func (r *Runner) risePerTick() float64 { return 1 - math.Pow(1-peakRise, float64(r.cfg.Rate)/60) }

func (r *Runner) releasePerTick() float64 { return math.Pow(r.cfg.Release, 60/float64(r.cfg.Rate)) }

// Bands returns the current smoothed bar values in [0, 1].
// The returned slice aliases Runner's buffer; callers that retain it must copy.
func (r *Runner) Bands() []float64 { return r.out }

// Config returns the effective config with defaults resolved.
func (r *Runner) Config() Config { return r.cfg }

// Backend reports the active FFT implementation ("simd" or "scalar").
func (r *Runner) Backend() string {
	if _, ok := r.an.(*simdAnalyzer); ok {
		return "simd"
	}

	return "scalar"
}

// Frame drains the tap, advances the window by wall-clock time, and updates the bands.
// It returns the same slice as [Runner.Bands] and is not safe for concurrent use.
func (r *Runner) Frame() []float64 {
	r.refill()

	step := r.stepSamples()
	if step > 0 {
		r.slide(step)
		r.an.Magnitudes(r.window, r.mags)
		r.sampleBands()
		r.toDB()
		r.trackPeak()
		r.emptyTicks = 0

		// Write fresh analysis directly; small wall-clock steps already smooth the motion.
		for b := range r.out {
			r.out[b] = r.bandValue(b)
		}

		return r.out
	}

	// Hold briefly between device pulls, then drain toward zero on sustained drought.
	r.emptyTicks++
	if r.emptyTicks <= r.holdTicks() {
		return r.out
	}
	drain := r.drainPerTick()
	for b := range r.out {
		r.out[b] *= drain
	}

	return r.out
}

// refill drains the tap into backlog and drops oldest samples if lag exceeds maxBacklog.
func (r *Runner) refill() {
	for {
		n := r.tap.Read(r.tmp)
		if n <= 0 {
			break
		}
		r.backlog = append(r.backlog, r.tmp[:n]...)
	}

	maxLen := len(r.window) + r.maxBacklog()
	if len(r.backlog) > maxLen {
		drop := len(r.backlog) - maxLen
		rest := copy(r.backlog, r.backlog[drop:])
		r.backlog = r.backlog[:rest]
	}
}

// stepSamples returns how many samples to advance based on elapsed wall time,
// clamped by maxStep and available backlog above the lag cushion.
func (r *Runner) stepSamples() int {
	now := r.now()
	if r.last.IsZero() {
		r.last = now

		return 0
	}

	elapsed := now.Sub(r.last)
	r.last = now
	if elapsed <= 0 {
		return 0
	}
	if elapsed > maxStep {
		elapsed = maxStep
	}

	step := int(elapsed.Nanoseconds() * int64(r.cfg.SampleRate) / int64(time.Second))

	// Preserve the jitter cushion and user delay ahead of the window.
	if room := len(r.backlog) - r.targetLag() - r.delay(); step > room {
		step = room
	}
	if step < 0 {
		step = 0
	}

	return step
}

func (r *Runner) targetLag() int { return r.cfg.SampleRate / targetLagDivisor }

func (r *Runner) delay() int { return r.cfg.SampleRate * r.cfg.DelayMs / 1000 }

func (r *Runner) maxBacklog() int { return r.cfg.SampleRate/maxLagDivisor + r.delay() }

// slide shifts n samples from backlog into window and compacts backlog in place to avoid allocations.
func (r *Runner) slide(n int) {
	if n <= 0 {
		return
	}

	p := r.backlog[:n]
	if n >= len(r.window) {
		copy(r.window, p[len(p)-len(r.window):])
		r.filled = len(r.window)
	} else {
		keep := r.filled + n
		if keep > len(r.window) {
			drop := keep - len(r.window)
			copy(r.window, r.window[drop:r.filled])
			r.filled -= drop
		}
		copy(r.window[r.filled:], p)
		r.filled += n
	}

	rest := copy(r.backlog, r.backlog[n:])
	r.backlog = r.backlog[:rest]
}

// bandValue normalizes band b from dB to [0, 1], returning 0 until a full FFT window is filled.
func (r *Runner) bandValue(b int) float64 {
	if r.filled < r.cfg.FFT {
		return 0
	}
	var v float64
	if r.cfg.Scale == ScaleFixed {
		v = (r.bands[b] - fixedFloorDB) / -fixedFloorDB
	} else {
		top := r.peak
		if math.IsInf(top, -1) {
			return 0
		}
		v = (r.bands[b] - (top - r.cfg.RangeDB)) / r.cfg.RangeDB
	}
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}

	return v
}

// toDB converts band magnitudes to dBFS, clamping near-zero values to a silent floor.
func (r *Runner) toDB() {
	const floorDB = -160

	for b := range r.bands {
		if r.bands[b] <= 1e-9 {
			r.bands[b] = floorDB

			continue
		}
		r.bands[b] = 20 * math.Log10(r.bands[b])
	}
}

// sampleBands linearly interpolates FFT bin magnitudes at each log-spaced bar center.
func (r *Runner) sampleBands() {
	hzPerBin := float64(r.cfg.SampleRate) / float64(r.cfg.FFT)
	top := len(r.mags) - 1

	for b, f := range r.centers {
		pos := f / hzPerBin
		if pos < 0 {
			pos = 0
		}
		if pos > float64(top) {
			pos = float64(top)
		}
		lo := int(pos)
		hi := min(lo+1, top)
		frac := pos - float64(lo)
		r.bands[b] = r.mags[lo] + (r.mags[hi]-r.mags[lo])*frac
	}

	// Smooth adjacent bars in linear magnitude before dB conversion so floored bins
	// do not pull down neighbouring peaks.
	copy(r.smooth, r.bands)
	last := len(r.bands) - 1
	for b := range r.bands {
		lo := r.smooth[max(b-1, 0)]
		hi := r.smooth[min(b+1, last)]
		r.bands[b] = 0.25*lo + 0.5*r.smooth[b] + 0.25*hi
	}
}

// trackPeak updates the dynamic scale peak with a 1% deadband and asymmetric rise/release.
func (r *Runner) trackPeak() {
	peak := math.Inf(-1)
	for _, db := range r.bands {
		if db > peak {
			peak = db
		}
	}
	if peak < -60 {
		peak = r.floor()
	}
	if math.IsInf(r.peak, -1) {
		r.peak = peak

		return
	}
	// Ignore jitter within 1% of the display range.
	if math.Abs(peak-r.peak)/r.cfg.RangeDB <= 0.01 {
		return
	}
	if peak > r.peak {
		r.peak += (peak - r.peak) * r.risePerTick()
	} else {
		rel := r.releasePerTick()
		r.peak = r.peak*rel + peak*(1-rel)
	}
}

func (r *Runner) floor() float64 { return -90 }

// barCenters returns log-spaced center frequencies across [cfg.MinHz, cfg.MaxHz].
func barCenters(cfg Config) []float64 {
	centers := make([]float64, cfg.Bars)
	ratio := cfg.MaxHz / cfg.MinHz

	for i := range centers {
		centers[i] = cfg.MinHz * math.Pow(ratio, (float64(i)+0.5)/float64(cfg.Bars))
	}

	return centers
}
