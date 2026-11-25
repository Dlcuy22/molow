package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlcuy22/molo/core"
	"github.com/dlcuy22/molo/decode"
	"github.com/dlcuy22/molo/provider"
	"github.com/dlcuy22/molow/internal/trackcache"
	"github.com/dlcuy22/ytm-go"
)

// aacFixtureBytes is the AAC body YouTube serves for a track it has no Opus
// rendition for: an MP4 whose first byte is the box size, not an EBML id.
func aacFixtureBytes(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile(engineFixture(t, "decode/testdata/sine_stereo_48k.m4a"))
	if err != nil {
		t.Fatalf("read the m4a fixture: %v", err)
	}

	return data
}

// aacStreams is a resolve result with no Opus format, the shape that broke
// playback before the container was chosen from the bytes.
func aacStreams(url string, durationMs int64) *ytm.Streams {
	return &ytm.Streams{
		VideoID:    "abc123",
		Title:      "AAC Title",
		Author:     "AAC Author",
		DurationMs: durationMs,
		Formats: []ytm.StreamFormat{{
			Itag:      140,
			MimeType:  `audio/mp4; codecs="mp4a.40.2"`,
			Codec:     ytm.CodecAAC,
			URL:       url,
			ExpiresAt: time.Now().Add(time.Hour),
		}},
	}
}

// aacMediaServer serves the AAC fixture on every path.
func aacMediaServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mp4")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// openAACSource resolves an AAC-only track against srv, with the seamless flag
// the caller asked for.
func openAACSource(t *testing.T, srv *httptest.Server, seamless bool) provider.Source {
	t.Helper()

	p := &ytmProvider{
		index:    newYTMIndex(),
		http:     srv.Client(),
		seamless: seamless,
		resolve: func(context.Context, string) (*ytm.Streams, error) {
			return aacStreams(srv.URL+"/media", 250), nil
		},
	}
	src, err := p.Open(context.Background(), "ytm:abc123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return src
}

// TestYTMProviderPlaysAACFormat proves the fix end to end: a track YouTube
// offers only as AAC resolves, probes its duration, and decodes real audio.
func TestYTMProviderPlaysAACFormat(t *testing.T) {
	srv := aacMediaServer(t, aacFixtureBytes(t))
	src := openAACSource(t, srv, false)

	dec, err := src.Opener(make(chan struct{}))
	if err != nil {
		t.Fatalf("Opener: %v", err)
	}
	defer dec.Close()

	if _, parser := decode.Describe(dec); parser != "go-m4a (mp4/aac-lc)" {
		t.Fatalf("parser = %q, want the m4a reader", parser)
	}
	if got := dec.Info().Format; got != core.CanonicalFormat {
		t.Fatalf("format = %+v, want canonical", got)
	}

	buf := make([]float32, 4096)
	n, err := dec.ReadFrames(buf)
	if err != nil || n == 0 {
		t.Fatalf("ReadFrames = (%d, %v), want audio", n, err)
	}

	info, err := src.Probe(core.DurationProbe)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// The 48 kHz fixture states its own length, so the catalogue duration must
	// not overwrite it.
	if want := int64(12000); info.TotalFrames != want {
		t.Fatalf("TotalFrames = %d, want %d", info.TotalFrames, want)
	}
}

// TestYTMSeamlessIsSkippedForAAC proves the forward-only path stays on Opus: an
// AAC track waits for the finished file and offers no Upgrade.
func TestYTMSeamlessIsSkippedForAAC(t *testing.T) {
	srv := aacMediaServer(t, aacFixtureBytes(t))
	src := openAACSource(t, srv, true)

	if src.Upgrade != nil {
		t.Fatal("an AAC source must not offer the forward-only Upgrade")
	}
	dec, err := src.Opener(make(chan struct{}))
	if err != nil {
		t.Fatalf("Opener: %v", err)
	}
	defer dec.Close()

	if _, parser := decode.Describe(dec); parser != "go-m4a (mp4/aac-lc)" {
		t.Fatalf("parser = %q, want the m4a reader", parser)
	}
}

// TestYTMWithDuration pins the fallback: a stream that cannot state a length
// takes the catalogue duration, and a stream that states one is left alone.
func TestYTMWithDuration(t *testing.T) {
	unknown := core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: -1}
	got := ytmWithDuration(unknown, 199000)
	// 44.1 kHz AAC states its length in the source domain, so the catalogue
	// value only fills the frame count for a stream that states nothing.
	if got.TotalFrames != 199*ytmSampleRate || got.Duration() != 199*time.Second {
		t.Fatalf("TotalFrames = %d dur = %s, want %d and 199s", got.TotalFrames, got.Duration(), 199*ytmSampleRate)
	}

	stated := core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: -1, SourceSamples: 8778576, SourceRate: 44100}
	if got := ytmWithDuration(stated, 199000); got.TotalFrames != -1 {
		t.Fatalf("a stated source length was overwritten: %d", got.TotalFrames)
	}
	if got := ytmWithDuration(unknown, 0); got.Duration() != 0 {
		t.Fatalf("no catalogue duration must leave the length unknown: %d", got.TotalFrames)
	}
}

// TestYTMCachedAACReportsDuration is the reported symptom: an AAC track opened
// from trackcache with no catalogue metadata still has to report a duration, or
// the seekbar is inert and the total reads 0:00.
func TestYTMCachedAACReportsDuration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc123.webm"), aacFixtureBytes(t), 0o644); err != nil {
		t.Fatalf("cache fixture: %v", err)
	}
	tracks, err := trackcache.Open(dir, 1<<20)
	if err != nil {
		t.Fatalf("trackcache: %v", err)
	}

	p := &ytmProvider{index: newYTMIndex(), tracks: tracks}
	src, err := p.Open(context.Background(), "ytm:abc123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cached, known := p.lastOpenCached("ytm:abc123")
	if !known || !cached {
		t.Fatalf("the cached file was not used: cached=%v known=%v", cached, known)
	}

	info, err := src.Probe(core.DurationProbe)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if want := 250 * time.Millisecond; info.Duration() != want {
		t.Fatalf("duration = %s, want %s", info.Duration(), want)
	}

	dec, err := src.Opener(make(chan struct{}))
	if err != nil {
		t.Fatalf("Opener: %v", err)
	}
	defer dec.Close()
	if _, parser := decode.Describe(dec); parser != "go-m4a (mp4/aac-lc)" {
		t.Fatalf("parser = %q, want the m4a reader", parser)
	}
}

// TestYTMUndecodableBodyIsNotCached proves a body no decoder accepts never
// enters trackcache, so a bad response cannot fail the track forever.
func TestYTMUndecodableBodyIsNotCached(t *testing.T) {
	dir := t.TempDir()
	tracks, err := trackcache.Open(dir, 1<<20)
	if err != nil {
		t.Fatalf("trackcache: %v", err)
	}

	srv := aacMediaServer(t, make([]byte, 8192))
	p := &ytmProvider{
		index:  newYTMIndex(),
		tracks: tracks,
		http:   srv.Client(),
		resolve: func(context.Context, string) (*ytm.Streams, error) {
			return aacStreams(srv.URL+"/media", 250), nil
		},
	}
	src, err := p.Open(context.Background(), "ytm:abc123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	dec, err := src.Opener(make(chan struct{}))
	if err == nil {
		dec.Close()
		t.Fatal("an all-zero body decoded, want a container error")
	}
	if !errors.Is(err, decode.ErrUnsupported) {
		t.Fatalf("Opener error = %v, want ErrUnsupported", err)
	}

	if tracks.Has("abc123") {
		t.Fatal("an undecodable body was cached")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache dir holds %d files, want none", len(entries))
	}
}
