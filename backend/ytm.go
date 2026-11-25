// Disclaimer
//
// No Affiliation:
// This project and its contributors are not affiliated with, authorized by,
// endorsed by, or in any way officially connected with Google LLC, YouTube, or
// any of their subsidiaries or affiliates. This is an independent, non-profit,
// and unofficial integration built for desktop playback.
//
// Trademarks:
// The names "Google" and "YouTube Music", as well as related names, marks,
// emblems, and images, are registered trademarks of their respective owners.
// Any use of these trademarks is for identification and reference purposes only
// and does not imply any association with the trademark holders. There is no
// intention to infringe upon these trademarks or cause harm to their holders.
//
// Limitation of Liability:
// This software is provided "AS IS", and you use it at your own risk. In no
// event shall the developers or contributors be liable for any claim, damages,
// or other liability, including any legal consequences, arising from, out of,
// or in connection with the software or the use or other dealings in it. The
// responsibility for any and all outcomes of using this software rests entirely
// with the user.
//
// YouTube Music as an audio source for the engine.
//
// This file is the consumer half of the provider seam: the engine knows nothing
// about YouTube, and everything network-facing lives here, in the UI module.
// The provider resolves a "ytm:<videoId>" reference into a Source whose Opener
// hands the engine a decoder over the track bytes.
//
// The track is downloaded directly to a temporary file on disk using bounded
// ranged GETs so long streams do not exhaust memory or trigger open-ended range
// throttling. Completed tracks within the trackcache budget are promoted in
// place via an atomic rename and opened as seekable local files.
//
// The web UI opts into the experimental seamless path (ytmSeamlessSwap): a
// forward-only decoder plays the download as it arrives on disk, and Upgrade
// swaps in the whole-track seekable decoder once the body is complete, so the
// first sound no longer waits for the last byte. Flipping that one value off
// disables the path everywhere in the UI and restores waiting for completion.

package backend

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molo/core"
	"github.com/dlcuy22/molo/decode"
	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molo/provider"
	"github.com/dlcuy22/molo/stream"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/trackcache"
	ytm "github.com/dlcuy22/ytm-go"
)

const (
	// ytmRefPrefix is the scheme that marks a reference as a YouTube Music
	// track. It is the only thing the engine sees, so it must not look like a
	// filesystem path.
	ytmRefPrefix = "ytm:"

	// ytmMaxTrackBytes bounds one track downloaded to disk. A 10-hour Opus set
	// fits well under this; the bound exists so a wrong Content-Length or an
	// endless body cannot fill the disk.
	ytmMaxTrackBytes = 1 << 30

	// ytmChunkBytes bounds one ranged GET. googlevideo throttles open-ended
	// "bytes=0-" requests on long streams to ~32 KiB/s, while bounded 8 MiB
	// chunks run at full speed and keep normal tracks to a single request.
	ytmChunkBytes = 8 << 20

	// ytmMaxThumbBytes bounds one thumbnail download. Art is decorative, so a
	// body past this is dropped rather than kept.
	ytmMaxThumbBytes = 4 << 20

	// ytmHTTPTimeout bounds one thumbnail or small non-chunked fetch.
	ytmHTTPTimeout = 60 * time.Second

	// ytmHeaderTimeout and ytmChunkTimeout bound response headers and each 8 MiB
	// media chunk individually so multi-hour tracks do not hit a whole-body limit.
	ytmHeaderTimeout = 15 * time.Second
	ytmChunkTimeout  = 60 * time.Second

	ytmChunkRetries = 2

	// ytmSampleRate is the rate the WebM/Opus reader always outputs. It is what
	// converts a duration in milliseconds to the engine's frame domain.
	ytmSampleRate = 48000

	// ytmIndexMax bounds the catalogue metadata cache. Search results fill it,
	// so without a bound a long session would grow it with every query.
	ytmIndexMax = 512

	// ytmSearchThumbMax and ytmSearchThumbEdge bound the in-memory LRU for
	// ephemeral Ctrl+F search result thumbnails so unselected hits never write
	// to artwork.Store on disk.
	ytmSearchThumbMax  = 64
	ytmSearchThumbEdge = 128

	// ytmPlaylistMaxTracks and ytmPlaylistMaxPages bound one playlist add. A
	// long playlist is still bounded work: the walk stops at the track cap, or
	// at the page cap if the catalogue keeps handing back continuations. The
	// bounds are stated rather than silent so a truncated add is a known limit.
	ytmPlaylistMaxTracks = 1000
	ytmPlaylistMaxPages  = 40

	// ytmSeamlessSwap is the one switch for the experimental fast-start path.
	// The web UI is the consumer and opts in here, at initialization: this value
	// is read by both the provider, which then offers the Upgrade, and the
	// engine, which then consumes it, so the two cannot drift. Flipping this one
	// value off disables the path everywhere in the UI and restores the
	// whole-track fetch.
	ytmSeamlessSwap = true
)

// ytmRef builds the queue reference for a video id. CleanSongID strips the
// "MPED" prefix some catalogue responses carry, so the same track has one ref
// however it was reached.
func ytmRef(id string) string { return ytmRefPrefix + ytm.CleanSongID(id) }

// ytmID reads the video id back out of a reference.
func ytmID(ref string) string { return ytm.CleanSongID(strings.TrimPrefix(ref, ytmRefPrefix)) }

// isYTMRef reports whether a queue entry belongs to this provider. It is the
// cheap check the session calls for every queued ref.
func isYTMRef(ref string) bool { return strings.HasPrefix(ref, ytmRefPrefix) }

// ytmTrack is what the UI knows about one video id: the catalogue metadata and,
// once it has been fetched, the artwork bytes. It is the row's identity, so it
// carries no URL the UI would have to fetch itself.
type ytmTrack struct {
	ID         string
	Title      string
	Artist     string
	Album      string
	DurationMs int64

	// AlbumLoaded records that the album was looked up, so an artist or track
	// with no album is not re-requested on every play. It is separate from Album
	// because "" is both "no album" and "not looked up yet".
	AlbumLoaded bool

	// ArtistID is the browse id of the first credited artist. It is the key the
	// avatar lookup uses, and it is kept even when no avatar has been resolved
	// yet, so the lookup can happen later.
	ArtistID string

	// ArtistThumbURL is the resolved avatar URL, or "" when the artist has none
	// or has not been looked up. It is a URL rather than bytes because only the
	// Discord presence consumes it, and Discord fetches an https URL itself.
	ArtistThumbURL string

	// ThumbnailURL is the high quality art URL from the catalogue. It is kept
	// so the art can be fetched lazily, when a row is drawn or the track is
	// played, rather than for every search result at once.
	ThumbnailURL string

	// CoverID is the canonical artwork store id when the cover is already cached
	// on disk, so catalogue rows and playback share one cache entry after restart.
	CoverID string

	// Thumb is the fetched art, filled on the first fetch and reused after.
	Thumb []byte
}

// coverID identifies the artwork for the UI. It prefers a canonical artwork
// store id, then the content hash once bytes are fetched, then a URL hash.
func (t ytmTrack) coverID() string {
	if t.CoverID != "" {
		return t.CoverID
	}
	if len(t.Thumb) > 0 {
		return artwork.ID(t.Thumb)
	}
	if t.ThumbnailURL != "" {
		return ytmURLID(t.ThumbnailURL)
	}

	return ""
}

// ytmURLID is a short, stable id for a thumbnail URL. It keeps the long signed
// URL out of the 4 Hz snapshot while still changing when the art changes.
func ytmURLID(url string) string {
	h := fnv.New64a()
	_, _ = io.WriteString(h, url)

	return "ytm-" + strconv.FormatUint(h.Sum64(), 16)
}

// ytmIndex caches the metadata of every video id the session has seen, so a
// queue row for a remote ref shows the real title, artist and art instead of
// the reference string. It is filled from search results and from the provider
// when a track is opened.
type ytmIndex struct {
	mu      sync.Mutex
	entries map[string]ytmTrack
}

func newYTMIndex() *ytmIndex {
	return &ytmIndex{entries: make(map[string]ytmTrack)}
}

// put records a track under its reference. An entry with no thumbnail URL keeps
// any art already fetched, so a later search hit cannot erase the bytes.
func (x *ytmIndex) put(t ytmTrack) {
	ref := ytmRef(t.ID)
	if ref == ytmRefPrefix {
		return
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	prev := x.entries[ref]
	if t.Title == "" {
		t.Title = prev.Title
	}
	if t.Artist == "" {
		t.Artist = prev.Artist
	}
	if t.DurationMs <= 0 {
		t.DurationMs = prev.DurationMs
	}
	if t.ThumbnailURL == "" {
		t.ThumbnailURL = prev.ThumbnailURL
	}
	if t.CoverID == "" {
		t.CoverID = prev.CoverID
	}
	if len(t.Thumb) == 0 {
		t.Thumb = prev.Thumb
	}
	// The album name, the artist id and its resolved avatar are filled in by
	// later, separate lookups, so a subsequent search hit that carries none of
	// them must not erase what is already known.
	if t.Album == "" {
		t.Album = prev.Album
	}
	if !t.AlbumLoaded {
		t.AlbumLoaded = prev.AlbumLoaded
	}
	if t.ArtistID == "" {
		t.ArtistID = prev.ArtistID
	}
	if t.ArtistThumbURL == "" {
		t.ArtistThumbURL = prev.ArtistThumbURL
	}
	x.entries[ref] = t
}

// setAlbum records the resolved album name on an existing entry, marking it
// looked up so a track with no album is not queried again.
func (x *ytmIndex) setAlbum(ref, album string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.entries[ref]
	if !ok {
		return
	}
	t.Album = album
	t.AlbumLoaded = true
	x.entries[ref] = t
}

// setArtist records the resolved avatar (and the artist id it belongs to) on an
// existing entry, leaving the rest alone.
func (x *ytmIndex) setArtist(ref, artistID, avatarURL string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.entries[ref]
	if !ok {
		return
	}
	if artistID != "" {
		t.ArtistID = artistID
	}
	t.ArtistThumbURL = avatarURL
	x.entries[ref] = t
}

// lookup returns the cached track for a reference.
func (x *ytmIndex) lookup(ref string) (ytmTrack, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.entries[ref]

	return t, ok
}

// setThumb records fetched artwork for a reference, leaving the rest of the
// entry alone.
func (x *ytmIndex) setThumb(ref string, thumb []byte) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.entries[ref]
	if !ok {
		return
	}
	t.Thumb = thumb
	x.entries[ref] = t
}

// prune bounds the index. It drops entries that are not in the protected set,
// and only once the cap is exceeded, so a reference cached for a search and
// not yet in the queue survives the gap between the play command and the
// control loop installing the queue.
func (x *ytmIndex) prune(protected map[string]struct{}) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.entries) <= ytmIndexMax {
		return
	}
	for ref := range x.entries {
		if len(x.entries) <= ytmIndexMax {
			return
		}
		if _, ok := protected[ref]; ok {
			continue
		}
		delete(x.entries, ref)
	}
}

// ytmProvider resolves YouTube Music references. It implements
// provider.AudioProvider.
type ytmProvider struct {
	index  *ytmIndex
	art    *artwork.Store
	tracks *trackcache.Store
	http   *http.Client

	// artists caches resolved artist avatars for the presence's small image. It
	// is separate from the track index because one artist can front many tracks.
	artists *ytmArtistIndex

	// avatarMu guards artistID -> artwork cover ID so repeated settings previews
	// hit the shared artwork store's bounded LRU without re-fetching HTTP bytes.
	avatarMu  sync.Mutex
	avatarIDs map[string]string

	// searchThumbs keeps ephemeral Ctrl+F search thumbnails in RAM only so
	// browsing search results never writes unselected artwork to disk.
	searchThumbMu sync.Mutex
	searchThumbs  *memThumbLRU

	// openCached records whether the latest Open for each ref hit trackcache
	// so the engine's DebugTrack log line reports cached=yes or cached=no.
	openCachedMu sync.Mutex
	openCached   map[string]bool

	// sheetMu guards the lyric sheets. The now-playing column reads a sheet
	// every time it redraws, and a sheet is immutable for a track, so resolving
	// one twice only costs requests. A nil entry records a track known to have
	// no lyrics, so that answer is not fetched again either.
	sheetMu sync.Mutex
	sheets  map[string]*ytm.Lyrics
	sheetAt []string

	log *slog.Logger

	// seamless selects the experimental fast-start path: Open returns a
	// forward-only Opener over a background download plus an Upgrade that wraps
	// the completed body in a seekable decoder. The UI sets it from
	// ytmSeamlessSwap, the single switch shared with the engine; with that off,
	// Open keeps the whole-track fetch exactly as before.
	seamless bool

	// chunkBytes overrides ytmChunkBytes in tests so multi-chunk 206 transfers
	// and mid-stream resume can be exercised on small fixtures.
	chunkBytes int64

	// resolve, search, loadArtist, loadPlaylist, loadMix, loadSong and lyrics
	// are the network calls, indirected so a test can drive the provider and the
	// service without touching YouTube. In production they are the client's own
	// methods; the client is also kept so the account methods stay reachable, and
	// so a stored session can be attached after startup without rebuilding the
	// provider.
	//
	// apiMu guards the whole set: a sign-in swaps every call at once while a
	// track may be opening or a search running, so readers take a snapshot
	// through calls rather than touch the fields directly.
	apiMu        sync.RWMutex
	client       *ytm.Client
	resolve      func(context.Context, string) (*ytm.Streams, error)
	search       func(context.Context, string, string, bool) (*ytm.SearchResults, error)
	loadArtist   func(context.Context, string) (*ytm.Artist, error)
	loadPlaylist func(context.Context, string, *ytm.BuiltInContinuation) (*ytm.Playlist, error)
	loadMix      func(context.Context, string, *ytm.BuiltInContinuation) (*ytm.Playlist, error)
	loadSong     func(context.Context, string) (*ytm.Song, error)
	lyrics       func(context.Context, string) (*ytm.Lyrics, error)

	// rotate replaces the anonymous session the client is using. YouTube refuses
	// a media URL by the visitor that asked for it, so a fresh visitor is what
	// an app restart gives and what a refused stream needs. It is a field so a
	// test can drive the recovery without a client.
	rotate func()

	// catalogTrack resolves metadata and cached CoverID from the SQLite library
	// when the in-memory index does not have the track yet (e.g. after restart).
	catalogTrack func(ref string) (ytmTrack, bool)

	// saveRemoteMeta backfills hydrated thumbnail URL, artist ID, and album
	// back into SQLite when a catalogued track predates those columns.
	saveRemoteMeta func(ref, thumbnailURL, artistID, album string)
}

// ytmCalls is one consistent snapshot of the provider's network surface. A
// sign-in replaces the client, and a reader that mixed an old resolve with a
// new search would be talking to two sessions at once.
type ytmCalls struct {
	resolve      func(context.Context, string) (*ytm.Streams, error)
	search       func(context.Context, string, string, bool) (*ytm.SearchResults, error)
	loadArtist   func(context.Context, string) (*ytm.Artist, error)
	loadPlaylist func(context.Context, string, *ytm.BuiltInContinuation) (*ytm.Playlist, error)
	loadMix      func(context.Context, string, *ytm.BuiltInContinuation) (*ytm.Playlist, error)
	loadSong     func(context.Context, string) (*ytm.Song, error)
	lyrics       func(context.Context, string) (*ytm.Lyrics, error)
}

// calls reads the current network surface under the lock.
func (p *ytmProvider) calls() ytmCalls {
	p.apiMu.RLock()
	defer p.apiMu.RUnlock()

	return ytmCalls{
		resolve:      p.resolve,
		search:       p.search,
		loadArtist:   p.loadArtist,
		loadPlaylist: p.loadPlaylist,
		loadMix:      p.loadMix,
		loadSong:     p.loadSong,
		lyrics:       p.lyrics,
	}
}

// ytmClient builds the client every network call goes through. The library logs
// recovered parse panics instead of returning an empty list in silence, but only
// when it has a logger.
func ytmClient(log *slog.Logger) *ytm.Client {
	client := ytm.NewClient()
	client.SetLogger(log)

	return client
}

func newYTMHTTPClient() *http.Client {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := tr.Clone()
		cloned.ResponseHeaderTimeout = ytmHeaderTimeout

		return &http.Client{Transport: cloned}
	}

	return &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: ytmHeaderTimeout},
	}
}

func newYTMProvider(index *ytmIndex, art *artwork.Store, seamless bool, log *slog.Logger) *ytmProvider {
	if log == nil {
		log = slog.Default()
	}
	p := &ytmProvider{
		index:        index,
		art:          art,
		artists:      newYTMArtistIndex(),
		avatarIDs:    make(map[string]string),
		searchThumbs: newMemThumbLRU(ytmSearchThumbMax),
		sheets:       make(map[string]*ytm.Lyrics),
		log:          log,
		http:         newYTMHTTPClient(),
		seamless:     seamless,
	}
	p.installClient(ytmClient(log))
	p.rotate = p.rotateIdentity

	return p
}

// installClient swaps the client and every call bound to it in one step, so a
// reader that took a snapshot through [ytmProvider.calls] never mixes two
// sessions.
func (p *ytmProvider) installClient(client *ytm.Client) {
	client.SetLogger(p.log)
	p.apiMu.Lock()
	defer p.apiMu.Unlock()
	p.client = client
	p.resolve = client.GetStream
	p.search = client.Search
	p.loadArtist = client.LoadArtist
	p.loadPlaylist = func(ctx context.Context, id string, cont *ytm.BuiltInContinuation) (*ytm.Playlist, error) {
		return client.LoadPlaylist(ctx, id, cont, nil, nil, false)
	}
	p.loadMix = client.GetMix
	p.loadSong = client.LoadSong
	p.lyrics = client.GetSongLyricsTimed
}

// rotateIdentity replaces the anonymous session. A stream refusal is bound to
// the visitor that minted the URL, and the catalogue keeps answering under the
// same visitor, so a new client is the restart the refusal needs. A signed-in
// session is carried over: playback is anonymous either way, but search and the
// library are not.
func (p *ytmProvider) rotateIdentity() {
	p.apiMu.RLock()
	var auth *ytm.AuthState
	if p.client != nil {
		auth = p.client.Auth()
	}
	p.apiMu.RUnlock()

	fresh := ytmClient(p.log)
	if auth != nil {
		fresh.SetAuth(auth)
	}
	p.installClient(fresh)
}

func (p *ytmProvider) Name() string { return "ytm" }

func (p *ytmProvider) Match(ref string) bool { return isYTMRef(ref) }

// Open resolves a reference into a playable Source. A track already cached on
// disk opens directly from the cache without calling YouTube. On a cache miss,
// the stream lookup and artwork fetch happen here on the build worker and the
// audio download runs in Opener, caching completed streams for later replays.
func (p *ytmProvider) Open(ctx context.Context, ref string) (provider.Source, error) {
	if err := ctx.Err(); err != nil {
		return provider.Source{}, err
	}
	id := ytmID(ref)
	if id == "" {
		return provider.Source{}, fmt.Errorf("ytm: %q carries no track id", ref)
	}

	if p.tracks != nil && p.tracks.Has(id) {
		p.setOpenCached(ref, true)
		p.logger().Debug("trackcache: hit", "id", id)
		track := p.hydrateTrackThumb(ctx, ref, p.trackFor(ref, nil))
		src := ytmSourceMeta(track)
		var infoMu sync.Mutex
		var openedInfo core.StreamInfo
		recordInfo := func(info core.StreamInfo) {
			info = ytmWithDuration(info, track.DurationMs)
			if info.Duration() <= 0 {
				return
			}
			infoMu.Lock()
			openedInfo = info
			infoMu.Unlock()
			if ms := info.Duration().Milliseconds(); ms > 0 {
				t, _ := p.index.lookup(ref)
				if t.DurationMs <= 0 {
					t.ID = firstNonEmpty(t.ID, id)
					t.DurationMs = ms
					p.index.put(t)
				}
			}
		}
		src.Opener = func(stop <-chan struct{}) (decode.Decoder, error) {
			if rc, _, ok := p.tracks.Get(id); ok {
				p.setOpenCached(ref, true)
				dec, err := openSeekableContainer(rc)
				if err != nil {
					_ = rc.Close()

					return nil, err
				}
				recordInfo(dec.Info())

				return wrapClosingDecoder(dec, rc), nil
			}

			// The file was evicted between Open and Opener; fall back to a
			// fresh remote fetch.
			p.setOpenCached(ref, false)
			p.logger().Debug("trackcache: evicted before open, falling back to network", "id", id)

			dec, err := p.fetchRemoteDecoder(ctx, stop, id)
			if err != nil {
				return nil, err
			}
			recordInfo(dec.Info())

			return dec, nil
		}
		baseProbe := src.Probe
		src.Probe = func(mode core.DurationMode) (core.StreamInfo, error) {
			infoMu.Lock()
			info := openedInfo
			infoMu.Unlock()
			if info.Duration() > 0 {
				return info, nil
			}
			if rc, _, ok := p.tracks.Get(id); ok {
				defer rc.Close()
				if dec, err := openSeekableContainer(rc); err == nil {
					info = dec.Info()
					_ = dec.Close()
					recordInfo(info)
					if info.Duration() > 0 {
						return info, nil
					}
				}
			}
			if baseProbe != nil {
				return baseProbe(mode)
			}

			return core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: -1}, nil
		}

		return src, nil
	}

	p.setOpenCached(ref, false)
	if p.tracks != nil {
		p.logger().Debug("trackcache: miss", "id", id)
	}

	calls := p.calls()
	streams, err := calls.resolve(ctx, id)
	if err != nil {
		return provider.Source{}, err
	}
	best, ok := streams.Best()
	if !ok {
		return provider.Source{}, fmt.Errorf("ytm: no audio format for %s", id)
	}

	track := p.hydrateTrackThumb(ctx, ref, p.trackFor(ref, streams))
	src := ytmSourceMeta(track)

	url := best.URL
	expires := best.ExpiresAt
	contentLen := best.ContentLength

	// renew resolves the track again and replaces the signed URL. It is shared by
	// the expiry refresh and the refusal retry, because both need a URL minted
	// under the session that is current now.
	renew := func() (string, time.Time, error) {
		fresh, err := p.calls().resolve(ctx, id)
		if err != nil {
			return "", time.Time{}, err
		}
		f, ok := fresh.Best()
		if !ok {
			return "", time.Time{}, fmt.Errorf("ytm: no audio format for %s", id)
		}
		url = f.URL
		expires = f.ExpiresAt
		if f.ContentLength > 0 {
			contentLen = f.ContentLength
		}

		return url, expires, nil
	}

	// refresh re-resolves only an expired URL: the streamer calls the opener
	// again on the reopen-and-discard seek fallback and on a decoder swap, so a
	// URL that still works must not cost a request.
	refresh := func() error {
		if expires.IsZero() || !time.Now().After(expires) {
			return nil
		}
		_, _, err := renew()

		return err
	}

	buf := newYTMStreamBuffer(id, p.tracks, p.logger())
	var once sync.Once
	start := func(stop <-chan struct{}) {
		once.Do(func() {
			if err := refresh(); err != nil {
				buf.finish(err)

				return
			}
			if err := buf.initFile(); err != nil {
				buf.finish(err)

				return
			}
			go func() {
				err := p.download(ctx, stop, url, expires, contentLen, ytmMaxTrackBytes, buf, renew)
				buf.finish(err)
			}()
		})
	}

	if src.Probe == nil {
		src.Probe = func(core.DurationMode) (core.StreamInfo, error) {
			if err := buf.wait(ctx); err != nil {
				return core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: -1}, err
			}
			dec, err := buf.openSeekableDecoder()
			if err != nil {
				return core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: -1}, err
			}
			info := ytmWithDuration(dec.Info(), track.DurationMs)
			_ = dec.Close()
			if ms := info.Duration().Milliseconds(); ms > 0 {
				t, _ := p.index.lookup(ref)
				if t.DurationMs <= 0 {
					t.ID = firstNonEmpty(t.ID, id)
					t.DurationMs = ms
					p.index.put(t)
				}
			}

			return info, nil
		}
	}

	if p.seamless && best.Codec == ytm.CodecOpus {
		// Fast start: the body downloads once to disk, in the background, while
		// the forward-only decoder reads from the front of the file, so audio
		// begins before the whole track is here. Upgrade waits for completion
		// and swaps in a seekable local file decoder with no second fetch.
		//
		// Only the WebM/Opus reader consumes a forward-only stream. A track
		// YouTube offers as AAC waits for the finished file instead, because the
		// AAC reader needs to seek.
		src.Opener = func(stop <-chan struct{}) (decode.Decoder, error) {
			start(stop)
			r, err := buf.reader()
			if err != nil {
				return nil, err
			}
			dec, err := decode.NewWebMOpusFactory().OpenReader(r)
			if err != nil {
				_ = r.Close()

				return nil, err
			}

			return wrapClosingDecoder(dec, r), nil
		}
		src.Upgrade = func(uctx context.Context) (stream.Opener, error) {
			// The opener normally starts the download already; starting here
			// too keeps a caller that upgrades first from waiting on a body
			// nobody fetched.
			start(nil)
			if err := buf.wait(uctx); err != nil {
				return nil, err
			}

			return func(<-chan struct{}) (decode.Decoder, error) {
				return buf.openSeekableDecoder()
			}, nil
		}

		return src, nil
	}

	src.Opener = func(stop <-chan struct{}) (decode.Decoder, error) {
		start(stop)
		if err := buf.waitStop(ctx, stop); err != nil {
			return nil, err
		}

		return buf.openSeekableDecoder()
	}

	return src, nil
}

func (p *ytmProvider) hydrateTrackThumb(ctx context.Context, ref string, track ytmTrack) ytmTrack {
	if len(track.Thumb) == 0 && track.CoverID != "" && p.art != nil {
		if data, _, ok := p.art.Bytes(track.CoverID); ok {
			track.Thumb = data
		}
	}
	if len(track.Thumb) == 0 && track.ThumbnailURL != "" {
		if thumb := p.thumbFor(ctx, track.ThumbnailURL); len(thumb) > 0 {
			track.Thumb = thumb
			if p.art != nil {
				if cid, err := p.art.Put(thumb); err == nil && cid != "" {
					track.CoverID = cid
				}
			}
			p.index.setThumb(ref, thumb)
		}
	}
	p.index.put(track)

	return track
}

func ytmSourceMeta(track ytmTrack) provider.Source {
	src := provider.Source{
		Meta: meta.Meta{
			Container: "webm",
			Codec:     "opus",
			Tags: meta.Tags{
				Title:  track.Title,
				Artist: track.Artist,
				Album:  track.Album,
			},
		},
	}
	// The now-playing artwork rides in the meta the way a tagged file's does,
	// so the existing cover path needs no special case for a remote track.
	if len(track.Thumb) > 0 {
		src.Meta.Tags.Cover = track.Thumb
		src.Meta.Tags.CoverMIME = http.DetectContentType(track.Thumb)
	}
	if frames := ytmFrames(track.DurationMs); frames >= 0 {
		src.Meta.Stream = core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: frames}
		src.Probe = func(core.DurationMode) (core.StreamInfo, error) {
			return core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: frames}, nil
		}
	}

	return src
}

func (p *ytmProvider) fetchRemoteDecoder(ctx context.Context, stop <-chan struct{}, id string) (decode.Decoder, error) {
	streams, err := p.calls().resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	best, ok := streams.Best()
	if !ok {
		return nil, fmt.Errorf("ytm: no audio format for %s", id)
	}
	buf := newYTMStreamBuffer(id, p.tracks, p.logger())
	if err := buf.initFile(); err != nil {
		buf.finish(err)

		return nil, err
	}
	renew := func() (string, time.Time, error) {
		fresh, rerr := p.calls().resolve(ctx, id)
		if rerr != nil {
			return "", time.Time{}, rerr
		}
		f, ok := fresh.Best()
		if !ok {
			return "", time.Time{}, fmt.Errorf("ytm: no audio format for %s", id)
		}

		return f.URL, f.ExpiresAt, nil
	}
	err = p.download(ctx, stop, best.URL, best.ExpiresAt, best.ContentLength, ytmMaxTrackBytes, buf, renew)
	buf.finish(err)
	if err != nil {
		return nil, err
	}

	return buf.openSeekableDecoder()
}

// openSeekableContainer opens the local file behind rc through the decode
// registry, so the container is chosen from the bytes. A fixed WebM reader
// rejects the AAC in MP4 that YouTube serves when a track has no Opus rendition,
// and the rejection outlives the attempt because the file gets cached.
func openSeekableContainer(rc io.ReadSeekCloser) (decode.Decoder, error) {
	named, ok := rc.(interface{ Name() string })
	if !ok {
		return decode.NewWebMOpusFactory().OpenReader(rc)
	}

	return decode.Default.Open(named.Name())
}

// ytmWithDuration fills an unknown stream length from the catalogue duration.
// The AAC renditions are 44.1 kHz, and the M4A reader reports those as an
// unknown frame count because their duration is not a whole number of canonical
// frames; a stream that already states a duration is left alone.
func ytmWithDuration(info core.StreamInfo, durationMs int64) core.StreamInfo {
	if info.Duration() <= 0 && durationMs > 0 {
		if info.Format.Rate <= 0 {
			info.Format = core.CanonicalFormat
		}
		info.TotalFrames = ytmFrames(durationMs)
	}

	return info
}

type closingDecoder struct {
	decode.Decoder
	closer io.Closer
}

func (d closingDecoder) Close() error {
	decErr := d.Decoder.Close()
	closeErr := d.closer.Close()
	if decErr != nil {
		return decErr
	}

	return closeErr
}

func (d closingDecoder) DecoderName() string {
	name, _ := decode.Describe(d.Decoder)

	return name
}

func (d closingDecoder) ParserName() string {
	_, parser := decode.Describe(d.Decoder)

	return parser
}

type seekableClosingDecoder struct {
	closingDecoder
	seeker decode.Seeker
}

func (d seekableClosingDecoder) SeekFrame(frame int64) error {
	return d.seeker.SeekFrame(frame)
}

func wrapClosingDecoder(dec decode.Decoder, c io.Closer) decode.Decoder {
	base := closingDecoder{Decoder: dec, closer: c}
	if s, ok := dec.(decode.Seeker); ok {
		return seekableClosingDecoder{closingDecoder: base, seeker: s}
	}

	return base
}

// playlist walks a playlist's pages, flattening each into the rows the service
// caches and queues, and reports the catalogue's own title for the list. The
// walk is bounded so a runaway continuation cannot spin: it stops at the track
// cap, or at the page cap if the catalogue keeps handing back tokens. A nil
// continuation ends it, so a single-page playlist costs one request.
//
// A mix id ("RD…") is walked the same way, but its pages come from the
// watch-next queue: the browse endpoint refuses the type.
func (p *ytmProvider) playlist(ctx context.Context, id string) ([]YTMResult, string, error) {
	var out []YTMResult
	name := ""
	seen := make(map[string]struct{})
	var cont *ytm.BuiltInContinuation
	calls := p.calls()
	load := calls.loadPlaylist
	if ytm.IsMixID(id) {
		load = calls.loadMix
	}

	for page := 0; page < ytmPlaylistMaxPages; page++ {
		pl, err := load(ctx, id, cont)
		if err != nil {
			return nil, "", err
		}
		if pl == nil {
			break
		}
		// The first page names the list. A continuation page repeats it, and a
		// mix may name a different station per page, so only the first counts.
		if name == "" {
			name = strings.TrimSpace(pl.Name)
		}
		for _, r := range ytmPlaylistResults(pl) {
			if _, ok := seen[r.VideoID]; ok {
				continue
			}
			seen[r.VideoID] = struct{}{}
			out = append(out, r)
			if len(out) >= ytmPlaylistMaxTracks {
				return out, name, nil
			}
		}
		if pl.Continuation == nil {
			break
		}
		cont = pl.Continuation
	}

	return out, name, nil
}

// trackFor merges the catalogue metadata already cached for a reference with
// what the stream lookup reported, preferring the catalogue copy: it is the one
// that carries the album and the artwork.
func (p *ytmProvider) trackFor(ref string, streams *ytm.Streams) ytmTrack {
	t, _ := p.index.lookup(ref)
	if p.catalogTrack != nil && (t.CoverID == "" || t.Title == "" || t.Artist == "" || t.Album == "" || t.ThumbnailURL == "" || t.ArtistID == "") {
		if cat, ok := p.catalogTrack(ref); ok {
			t.Title = firstNonEmpty(t.Title, cat.Title)
			t.Artist = firstNonEmpty(t.Artist, cat.Artist)
			t.Album = firstNonEmpty(t.Album, cat.Album)
			t.CoverID = firstNonEmpty(t.CoverID, cat.CoverID)
			t.ThumbnailURL = firstNonEmpty(t.ThumbnailURL, cat.ThumbnailURL)
			t.ArtistID = firstNonEmpty(t.ArtistID, cat.ArtistID)
			if t.DurationMs <= 0 {
				t.DurationMs = cat.DurationMs
			}
		}
	}
	if streams != nil {
		t.ID = firstNonEmpty(t.ID, streams.VideoID, ytmID(ref))
		t.Title = firstNonEmpty(t.Title, streams.Title)
		t.Artist = firstNonEmpty(t.Artist, streams.Author)
		if t.DurationMs <= 0 {
			t.DurationMs = streams.DurationMs
		}
	} else {
		t.ID = firstNonEmpty(t.ID, ytmID(ref))
	}

	return t
}

// artistArtFor resolves the avatar URL for a cached track, looking it up and
// caching it on the first ask. It is called from the presence update, off the
// playback path, so the browse round trip never delays a track's start.
func (p *ytmProvider) artistArtFor(ctx context.Context, ref string) string {
	t, ok := p.index.lookup(ref)
	if !ok {
		return ""
	}
	if t.ArtistThumbURL != "" {
		return t.ArtistThumbURL
	}
	url := p.artistArt(ctx, t.ArtistID)
	if url != "" {
		p.index.setArtist(ref, t.ArtistID, url)
	}

	return url
}

// hydrateSongMeta ensures a cached YTM track has its thumbnail URL, artist ID,
// and album populated. When any of those is missing and the watch endpoint has
// not been queried yet, one loadSong call fills all three and backfills SQLite.
func (p *ytmProvider) hydrateSongMeta(ctx context.Context, ref string) ytmTrack {
	t, ok := p.index.lookup(ref)
	if !ok && p.catalogTrack != nil {
		if cat, found := p.catalogTrack(ref); found {
			t = cat
			p.index.put(cat)
			t, _ = p.index.lookup(ref)
		}
	}
	t.ID = firstNonEmpty(t.ID, ytmID(ref))
	if t.ID == "" {
		return t
	}
	if t.AlbumLoaded || (t.ThumbnailURL != "" && t.ArtistID != "" && t.Album != "") {
		return t
	}

	loadSong := p.calls().loadSong
	if loadSong == nil {
		return t
	}

	song, err := loadSong(ctx, t.ID)
	if err != nil || song == nil {
		// Leave AlbumLoaded false on a transient failure so a later play retries.
		return t
	}

	t.ThumbnailURL = firstNonEmpty(t.ThumbnailURL, ytmThumb(song.Thumbnail))
	t.ArtistID = firstNonEmpty(t.ArtistID, ytmFirstArtistID(song.Artists))
	t.Album = firstNonEmpty(t.Album, ytmAlbum(song.Album))
	t.AlbumLoaded = true
	p.index.put(t)
	if p.saveRemoteMeta != nil {
		p.saveRemoteMeta(ref, t.ThumbnailURL, t.ArtistID, t.Album)
	}

	return t
}

// albumFor returns the album name for a cached track, looking it up once and
// caching the outcome.
func (p *ytmProvider) albumFor(ctx context.Context, ref string) string {
	t, ok := p.index.lookup(ref)
	if !ok {
		return ""
	}
	if t.AlbumLoaded || t.Album != "" {
		return t.Album
	}

	return p.hydrateSongMeta(ctx, ref).Album
}

// thumbFor fetches one thumbnail, best effort. Art is decorative, so a failure
// returns nothing rather than failing the track. ctx is honoured so a shutdown
// does not wait out the HTTP timeout on a decorative fetch.
func (p *ytmProvider) thumbFor(ctx context.Context, url string) []byte {
	for _, cand := range thumbCandidates(url) {
		if ctx.Err() != nil {
			return nil
		}
		data, err := p.fetch(ctx, nil, cand, ytmMaxThumbBytes)
		if err != nil || len(data) == 0 {
			continue
		}

		return data
	}

	return nil
}

// ytvStillURL matches the video-host still forms (".../vi/ID/NAME.jpg"), whose
// size prefix can be traded for a larger one. Signed song-artwork and avatar
// URLs must not be touched, so only this path shape matches.
var ytvStillURL = regexp.MustCompile(`(https?://[^/]+/vi/[^/?]+/)[a-z0-9]+\.jpg`)

// thumbCandidates lists the URLs worth trying for one thumbnail, best first.
// A video-category track lists only its feed thumbnails, such as the
// letterboxed 480x360 hqdefault, while the host keeps larger stills the
// listing never mentions. The listed URL stays last, since it always works.
func thumbCandidates(url string) []string {
	m := ytvStillURL.FindStringSubmatchIndex(url)
	if m == nil {
		return []string{url}
	}
	base := url[:m[3]]
	tail := url[m[1]:]

	return []string{base + "maxresdefault.jpg" + tail, base + "sddefault.jpg" + tail, url}
}

// errYTMRefused marks a media request the host answered with 403. The refusal
// belongs to the anonymous visitor that minted the URL, so the only retry that
// helps is a new session and a fresh URL, never the same request again.
var errYTMRefused = errors.New("ytm: media request refused")

// fetch reads one small URL whole into memory through a single ranged GET,
// bounded by limit and ytmHTTPTimeout.
func (p *ytmProvider) fetch(parent context.Context, stop <-chan struct{}, url string, limit int64) ([]byte, error) {
	var data []byte
	err := p.doRange(parent, stop, url, "bytes=0-", ytmHTTPTimeout, func(resp *http.Response) error {
		var rerr error
		data, rerr = io.ReadAll(io.LimitReader(resp.Body, limit+1))

		return rerr
	})
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("ytm: media body exceeds %d bytes", limit)
	}

	return data, nil
}

// download streams one URL into buf in bounded Range chunks, stopping at limit
// bytes. Bounded ranges avoid googlevideo's open-ended throttle on long tracks,
// and resuming from offset lets transient chunk errors or a rotated session
// continue without re-fetching earlier bytes.
func (p *ytmProvider) download(
	parent context.Context,
	stop <-chan struct{},
	url string,
	expires time.Time,
	contentLen int64,
	limit int64,
	buf *ytmStreamBuffer,
	renew func() (string, time.Time, error),
) error {
	if parent == nil {
		parent = context.Background()
	}

	var offset int64
	total := contentLen
	rotated := false
	retries := 0
	chunkSize := p.chunkSize()
	chunk := make([]byte, 64<<10)

	for {
		if err := parent.Err(); err != nil {
			return err
		}
		select {
		case <-stop:
			return context.Canceled
		default:
		}

		if !expires.IsZero() && time.Now().After(expires) && renew != nil {
			nextURL, nextExp, err := renew()
			if err != nil {
				return err
			}
			url = nextURL
			expires = nextExp
		}

		end := offset + chunkSize - 1
		if total > 0 && end >= total {
			end = total - 1
		}
		rangeHdr := fmt.Sprintf("bytes=%d-%d", offset, end)

		var chunkRead int64
		var isPartial bool
		var serverTotal int64

		err := p.doRange(parent, stop, url, rangeHdr, ytmChunkTimeout, func(resp *http.Response) error {
			if resp.StatusCode == http.StatusOK {
				if offset > 0 {
					return fmt.Errorf("ytm: server ignored Range at offset %d", offset)
				}
			} else {
				isPartial = true
				if start, _, crTotal, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
					if start != offset {
						return fmt.Errorf("ytm: unexpected range start %d, want %d", start, offset)
					}
					serverTotal = crTotal
				}
			}

			for {
				n, rerr := resp.Body.Read(chunk)
				if n > 0 {
					if offset+chunkRead+int64(n) > limit {
						return fmt.Errorf("ytm: media body exceeds %d bytes", limit)
					}
					if werr := buf.append(chunk[:n]); werr != nil {
						return werr
					}
					chunkRead += int64(n)
				}
				if rerr != nil {
					if errors.Is(rerr, io.EOF) {
						return nil
					}

					return rerr
				}
			}
		})

		if errors.Is(err, errYTMRefused) && !rotated && renew != nil {
			rotated = true
			nextURL, nextExp, rerr := p.recoverRefused(renew)
			if rerr != nil {
				return rerr
			}
			url = nextURL
			expires = nextExp

			continue
		}

		if err != nil {
			// Resume partial-content transfers on transient chunk errors so a
			// multi-hour download does not lose minutes of progress; full-body
			// 200 responses are not resumed to avoid duplicating bytes.
			if isPartial && retries < ytmChunkRetries && parent.Err() == nil && !stopClosed(stop) {
				offset += chunkRead
				retries++
				if renew != nil && !expires.IsZero() && time.Now().After(expires) {
					if nextURL, nextExp, rerr := renew(); rerr == nil {
						url = nextURL
						expires = nextExp
					}
				}

				continue
			}

			return err
		}

		if !isPartial {
			return nil
		}
		if chunkRead == 0 {
			return io.ErrUnexpectedEOF
		}
		offset += chunkRead
		if total == 0 && serverTotal > 0 {
			total = serverTotal
		}
		if (total > 0 && offset >= total) || (total == 0 && chunkRead < chunkSize) {
			return nil
		}
		retries = 0
	}
}

func (p *ytmProvider) chunkSize() int64 {
	if p != nil && p.chunkBytes > 0 {
		return p.chunkBytes
	}

	return ytmChunkBytes
}

func stopClosed(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func parseContentRange(hdr string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(hdr), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	rangePart, totalPart, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	startStr, endStr, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, 0, 0, false
	}
	s, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	e, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	if totalPart != "*" {
		t, err := strconv.ParseInt(totalPart, 10, 64)
		if err != nil {
			return 0, 0, 0, false
		}
		total = t
	}

	return s, e, total, true
}

// recoverRefused answers a refused stream with the change that can help: a new
// anonymous session, so the next resolve mints a URL under a visitor YouTube has
// not refused. renew swaps the signed URL. This is what restarting the app did,
// without the restart.
func (p *ytmProvider) recoverRefused(renew func() (string, time.Time, error)) (string, time.Time, error) {
	p.logger().Warn("ytm: stream refused, starting a new session")
	if p.rotate != nil {
		p.rotate()
	}

	return renew()
}

// doRange issues one ranged GET bounded by timeout and stop, handing the live
// response to consume. A 403 returns errYTMRefused from the headers alone.
func (p *ytmProvider) doRange(
	parent context.Context,
	stop <-chan struct{},
	url string,
	rangeHdr string,
	timeout time.Duration,
	consume func(*http.Response) error,
) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if stop != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", rangeHdr)

	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", errYTMRefused, resp.Status)
		}

		return fmt.Errorf("ytm: media request returned %s", resp.Status)
	}

	return consume(resp)
}

// ytmStreamBuffer streams one downloaded track into a temporary file on disk.
// A single producer writes chunks sequentially; forward readers open their own
// read handle and park on cond until written advances past their cursor or the
// download ends. When all readers close after a clean finish, a file within
// the cache budget is promoted atomically into trackcache.
type ytmStreamBuffer struct {
	mu          sync.Mutex
	cond        *sync.Cond
	id          string
	tracks      *trackcache.Store
	log         *slog.Logger
	dir         string
	path        string
	writer      *os.File
	written     int64
	done        bool
	err         error
	verified    bool
	committed   bool
	cleaned     bool
	hadSeekable bool
	readers     int
}

func newYTMStreamBuffer(id string, tracks *trackcache.Store, log *slog.Logger) *ytmStreamBuffer {
	dir := ""
	if tracks != nil {
		dir = tracks.Dir()
	}
	b := &ytmStreamBuffer{
		id:     id,
		tracks: tracks,
		log:    log,
		dir:    dir,
	}
	b.cond = sync.NewCond(&b.mu)

	return b
}

func (b *ytmStreamBuffer) initFile() error {
	pattern := "molow-ytm-*.tmp"
	if b.id != "" {
		pattern = b.id + ".tmp*"
	}
	f, err := os.CreateTemp(b.dir, pattern)
	if err != nil {
		return err
	}
	path := f.Name()
	runtime.AddCleanup(b, func(p string) {
		_ = os.Remove(p)
	}, path)

	b.mu.Lock()
	b.path = path
	b.writer = f
	b.mu.Unlock()

	return nil
}

// append writes one chunk to the temp file and wakes any reader waiting on it.
func (b *ytmStreamBuffer) append(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	b.mu.Lock()
	w := b.writer
	b.mu.Unlock()
	if w == nil {
		return errors.New("ytm: stream writer closed")
	}
	if _, err := w.Write(p); err != nil {
		return err
	}
	b.mu.Lock()
	b.written += int64(len(p))
	b.cond.Broadcast()
	b.mu.Unlock()

	return nil
}

// finish closes the writer and marks the end of the download. err, when
// non-nil, is surfaced to readers once all written bytes are drained.
func (b *ytmStreamBuffer) finish(err error) {
	b.mu.Lock()
	w := b.writer
	b.writer = nil
	b.done = true
	b.err = err
	b.cond.Broadcast()
	b.mu.Unlock()

	if w != nil {
		if cerr := w.Close(); cerr != nil && err == nil {
			b.mu.Lock()
			b.err = cerr
			b.mu.Unlock()
		}
	}

	b.mu.Lock()
	promoted, written := b.maybeFinalizeLocked()
	b.mu.Unlock()

	b.logStored(promoted, written)
}

func (b *ytmStreamBuffer) logStored(promoted bool, written int64) {
	if !promoted || b.tracks == nil || b.log == nil {
		return
	}
	st := b.tracks.Stats()
	b.log.Debug("trackcache: stored", "id", b.id, "bytes", written, "files", st.Count, "totalBytes", st.TotalBytes)
}

// maybeFinalizeLocked promotes the finished temp file into trackcache once no
// reader still holds the temp path open (Windows refuses os.Rename over an
// open handle). Unpromoted temp files stay available for reopen/Upgrade until
// a seekable reader closes or the download fails.
func (b *ytmStreamBuffer) maybeFinalizeLocked() (bool, int64) {
	if !b.done || b.readers > 0 || b.cleaned || b.committed || b.path == "" {
		return false, 0
	}
	if b.err == nil && b.verified && b.tracks != nil && b.written > 0 {
		promoted, err := b.tracks.CommitFile(b.id, b.path)
		if err != nil {
			if b.log != nil {
				b.log.Warn("trackcache: store failed", "id", b.id, "error", err)
			}
		} else if promoted {
			b.committed = true
			b.path = ""

			return true, b.written
		}
	}

	if b.err != nil || b.hadSeekable {
		_ = os.Remove(b.path)
		b.cleaned = true
		b.path = ""
	}

	return false, 0
}

func (b *ytmStreamBuffer) releaseReader() {
	b.mu.Lock()
	if b.readers > 0 {
		b.readers--
	}
	promoted, written := b.maybeFinalizeLocked()
	b.mu.Unlock()

	b.logStored(promoted, written)
}

// reader returns a fresh forward-only reader starting at byte zero.
func (b *ytmStreamBuffer) reader() (io.ReadCloser, error) {
	b.mu.Lock()
	if b.done && b.err != nil {
		err := b.err
		b.mu.Unlock()

		return nil, err
	}
	path := b.path
	if path == "" && b.committed && b.tracks != nil {
		id := b.id
		tracks := b.tracks
		b.mu.Unlock()
		if rc, _, ok := tracks.Get(id); ok {
			return rc, nil
		}

		return nil, errors.New("ytm: cached track evicted before open")
	}
	if path == "" {
		b.mu.Unlock()

		return nil, errors.New("ytm: stream file unavailable")
	}
	b.readers++
	b.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		b.releaseReader()

		return nil, err
	}

	return &ytmStreamReader{buf: b, f: f}, nil
}

// wait blocks until the download finishes or ctx is done.
func (b *ytmStreamBuffer) wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Waking on cancellation takes the lock, so the wake cannot slip between
	// the check in the loop and the park; Broadcast alone would race.
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer stop()

	b.mu.Lock()
	defer b.mu.Unlock()
	for !b.done {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.cond.Wait()
	}

	return b.err
}

func (b *ytmStreamBuffer) waitStop(ctx context.Context, stop <-chan struct{}) error {
	if stop == nil {
		return b.wait(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-stop:
			cancel()
		case <-done:
		}
	}()

	return b.wait(wctx)
}

func (b *ytmStreamBuffer) openSeekable() (io.ReadSeekCloser, error) {
	b.mu.Lock()
	if !b.done {
		b.mu.Unlock()

		return nil, errors.New("ytm: stream not finished")
	}
	if b.err != nil {
		err := b.err
		b.mu.Unlock()

		return nil, err
	}
	if b.committed && b.tracks != nil {
		id := b.id
		tracks := b.tracks
		b.mu.Unlock()
		if rc, _, ok := tracks.Get(id); ok {
			return rc, nil
		}

		return nil, errors.New("ytm: cached track evicted before open")
	}
	path := b.path
	if path == "" {
		b.mu.Unlock()

		return nil, errors.New("ytm: stream file unavailable")
	}
	b.hadSeekable = true
	b.readers++
	b.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		b.releaseReader()

		return nil, err
	}

	return &ytmTempReader{File: f, buf: b}, nil
}

func (b *ytmStreamBuffer) openSeekableDecoder() (decode.Decoder, error) {
	rc, err := b.openSeekable()
	if err != nil {
		return nil, err
	}
	dec, err := openSeekableContainer(rc)
	if err != nil {
		_ = rc.Close()

		return nil, err
	}
	// A file no decoder accepted must not enter trackcache: the bytes would be
	// replayed on every later open, and the failure would outlive a restart.
	b.mu.Lock()
	b.verified = true
	b.mu.Unlock()

	return wrapClosingDecoder(dec, rc), nil
}

// ytmStreamReader is the forward-only view the WebM factory sees: it implements
// Read and Close but not io.Seeker, so playback starts while chunks land on disk.
type ytmStreamReader struct {
	buf  *ytmStreamBuffer
	f    *os.File
	off  int64
	once sync.Once
}

func (r *ytmStreamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.buf.mu.Lock()
	for r.off >= r.buf.written && !r.buf.done {
		r.buf.cond.Wait()
	}
	avail := r.buf.written - r.off
	done := r.buf.done
	berr := r.buf.err
	r.buf.mu.Unlock()

	if avail <= 0 {
		if done && berr != nil {
			return 0, berr
		}

		return 0, io.EOF
	}
	if int64(len(p)) > avail {
		p = p[:avail]
	}
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	if n > 0 {
		return n, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if done && berr != nil {
		return 0, berr
	}

	return 0, io.EOF
}

func (r *ytmStreamReader) Close() error {
	var err error
	r.once.Do(func() {
		err = r.f.Close()
		r.buf.releaseReader()
	})

	return err
}

type ytmTempReader struct {
	*os.File
	buf  *ytmStreamBuffer
	once sync.Once
}

func (r *ytmTempReader) Close() error {
	var err error
	r.once.Do(func() {
		err = r.File.Close()
		r.buf.releaseReader()
	})

	return err
}

// fetchThumb fills in the artwork of a cached track, so a queue row can draw it
// without a second lookup. It is called off the lock, from the UI thread.
func (p *ytmProvider) fetchThumb(ref string) []byte {
	t, ok := p.index.lookup(ref)
	if !ok {
		return nil
	}
	if len(t.Thumb) > 0 {
		return t.Thumb
	}
	data := p.thumbFor(context.Background(), t.ThumbnailURL)
	if len(data) > 0 {
		p.index.setThumb(ref, data)
	}

	return data
}

// ytmFrames converts a duration in milliseconds to the engine's 48 kHz frame
// domain. An unknown duration is -1, which the engine reports as "not yet
// known" rather than as a zero length track.
func ytmFrames(durationMs int64) int64 {
	if durationMs <= 0 {
		return -1
	}

	return durationMs * ytmSampleRate / 1000
}

// YTMResult is one search hit, flattened for the frontend. Kind is the label a
// row shows; only a song is playable today, and a row says so rather than
// offering a control that does nothing.
type YTMResult struct {
	VideoID    string `json:"videoId"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	DurationMs int64  `json:"durationMs"`
	Kind       string `json:"kind"`
	Explicit   bool   `json:"explicit"`
	Thumbnail  string `json:"thumbnail"`
	// ArtistID is the first credited artist's browse id, carried so the
	// presence's avatar lookup has a key without a second search. It is empty
	// when the catalogue named no artist.
	ArtistID string `json:"artistId"`
	Playable bool   `json:"playable"`
}

// ytmResults flattens a search response into the rows the palette shows, songs
// first. A song from the catalogue is playable; an album, artist or playlist is
// listed with its kind and marked not playable, so the palette can show it
// without pretending it can be played. A video is playable too, but it is a
// video, so it sorts after every song.
//
// Order is stable within each bucket, and duplicates are dropped, because the
// same song can appear in more than one shelf.
func ytmResults(res *ytm.SearchResults) []YTMResult {
	if res == nil {
		return nil
	}

	var songs, videos, others []YTMResult
	seen := make(map[string]struct{})

	add := func(r YTMResult, bucket *[]YTMResult) {
		// Dedupe on the item's identity alone, not on the kind: the same song
		// can be offered by a song shelf and a video shelf at once, and both
		// map to one video id. The id is the identity; the title is the
		// fallback for an item the catalogue gave no id.
		key := r.VideoID
		if key == "" {
			key = "\x00" + r.Title
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*bucket = append(*bucket, r)
	}

	for _, cat := range res.Categories {
		for _, item := range cat.Layout.Items {
			switch it := item.(type) {
			case *ytm.Song:
				if it == nil {
					continue
				}
				r := YTMResult{
					VideoID:    ytm.CleanSongID(it.ID),
					Title:      it.Name,
					Artist:     ytmArtists(it.Artists),
					Album:      ytmAlbum(it.Album),
					DurationMs: it.DurationMs,
					Kind:       "Song",
					Explicit:   it.IsExplicit,
					Thumbnail:  ytmThumb(it.Thumbnail),
					ArtistID:   ytmFirstArtistID(it.Artists),
					Playable:   true,
				}
				if it.Type == ytm.SongTypeVideo {
					r.Kind = "Video"
					add(r, &videos)

					continue
				}
				add(r, &songs)
			case *ytm.Playlist:
				if it == nil {
					continue
				}
				kind := "Playlist"
				if it.Type == ytm.PlaylistTypeAlbum {
					kind = "Album"
				}
				add(YTMResult{
					VideoID:   it.ID,
					Title:     it.Name,
					Artist:    ytmArtists(it.Artists),
					Kind:      kind,
					Thumbnail: ytmThumb(it.Thumbnail),
				}, &others)
			case *ytm.Artist:
				if it == nil {
					continue
				}
				add(YTMResult{
					VideoID:   it.ID,
					Title:     it.Name,
					Kind:      "Artist",
					Thumbnail: ytmThumb(it.Thumbnail),
				}, &others)
			}
		}
	}

	out := make([]YTMResult, 0, len(songs)+len(videos)+len(others))
	out = append(out, songs...)
	out = append(out, videos...)

	return append(out, others...)
}

// ytmPlaylistID extracts a playlist id from the input a listener gives: the
// share URL they copy, a "VL"-prefixed browse id, or a bare id. It returns ""
// when nothing usable is there.
func ytmPlaylistID(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	// A share URL carries the id as its list= parameter; the query string is
	// the only place the id is guaranteed to be, so it is read first.
	if i := strings.Index(input, "list="); i != -1 {
		input = input[i+len("list="):]
		if j := strings.IndexByte(input, '&'); j != -1 {
			input = input[:j]
		}
	}
	// CleanPlaylistID strips the "MPSP" prefix the editor uses; the browse
	// call adds "VL" itself, so a bare id is exactly what LoadPlaylist wants.
	return ytm.CleanPlaylistID(strings.TrimSpace(input))
}

// ytmPlaylistResults flattens a loaded playlist's tracks into the same rows a
// search yields, so the catalogue index and the queue share one shape. Order is
// the catalogue's, and a repeated id is kept once: a playlist can list a track
// twice, but the queue should not.
func ytmPlaylistResults(p *ytm.Playlist) []YTMResult {
	if p == nil {
		return nil
	}
	out := make([]YTMResult, 0, len(p.Items))
	seen := make(map[string]struct{}, len(p.Items))
	for _, it := range p.Items {
		id := ytm.CleanSongID(it.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		album := ytmAlbum(it.Album)
		if album == "" && p.Type == ytm.PlaylistTypeAlbum {
			// An album's tracks often carry no per-track album credit; the
			// playlist name is the album, which is a fact, not a guess.
			album = strings.TrimSpace(p.Name)
		}
		out = append(out, YTMResult{
			VideoID:    id,
			Title:      it.Name,
			Artist:     ytmArtists(it.Artists),
			Album:      album,
			DurationMs: it.DurationMs,
			Kind:       "Song",
			Explicit:   it.IsExplicit,
			Thumbnail:  ytmThumb(it.Thumbnail),
			ArtistID:   ytmFirstArtistID(it.Artists),
			Playable:   true,
		})
	}

	return out
}

// ytmArtists joins the credited artists, dropping blanks so a partly populated
// credit does not render a dangling separator.
func ytmArtists(artists []ytm.Artist) string {
	names := make([]string, 0, len(artists))
	for _, a := range artists {
		if name := strings.TrimSpace(a.Name); name != "" {
			names = append(names, name)
		}
	}

	return strings.Join(names, ", ")
}

// ytmFirstArtistID returns the browse id of the first credited artist, which is
// the key an avatar lookup uses. It is empty when no credit carries an id.
func ytmFirstArtistID(artists []ytm.Artist) string {
	for _, a := range artists {
		if id := strings.TrimSpace(a.ID); id != "" {
			return id
		}
	}

	return ""
}

// ytmAlbum names the album of a song, when the catalogue listed one.
func ytmAlbum(album *ytm.Playlist) string {
	if album == nil {
		return ""
	}

	return strings.TrimSpace(album.Name)
}

// ytmThumb resolves the highest quality art URL, or "" when there is none.
func ytmThumb(t *ytm.ThumbnailProvider) string {
	if t == nil {
		return ""
	}

	return t.GetThumbnailURL(ytm.ThumbnailQualityHigh)
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}

type memThumb struct {
	key  string
	data []byte
	mime string
}

type memThumbLRU struct {
	cap   int
	order *list.List
	items map[string]*list.Element
}

func newMemThumbLRU(capacity int) *memThumbLRU {
	return &memThumbLRU{
		cap:   capacity,
		order: list.New(),
		items: make(map[string]*list.Element, capacity),
	}
}

func (c *memThumbLRU) get(key string) ([]byte, string, bool) {
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		v := elem.Value.(*memThumb)

		return v.data, v.mime, true
	}

	return nil, "", false
}

func (c *memThumbLRU) put(key string, data []byte, mime string) {
	if elem, ok := c.items[key]; ok {
		v := elem.Value.(*memThumb)
		v.data = data
		v.mime = mime
		c.order.MoveToFront(elem)

		return
	}
	if c.order.Len() >= c.cap {
		if back := c.order.Back(); back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(*memThumb).key)
		}
	}
	elem := c.order.PushFront(&memThumb{key: key, data: data, mime: mime})
	c.items[key] = elem
}

// searchThumbBytes fetches and downscales a search result thumbnail into the
// bounded in-memory LRU without writing to artwork.Store on disk.
func (p *ytmProvider) searchThumbBytes(ctx context.Context, ref, url string) ([]byte, string, bool) {
	if ref == "" || url == "" {
		return nil, "", false
	}

	p.searchThumbMu.Lock()
	if p.searchThumbs == nil {
		p.searchThumbs = newMemThumbLRU(ytmSearchThumbMax)
	}
	if data, mime, ok := p.searchThumbs.get(ref); ok {
		p.searchThumbMu.Unlock()
		p.logger().Debug("artwork: search thumb hit", "ref", ref)

		return data, mime, true
	}
	p.searchThumbMu.Unlock()

	raw := p.thumbFor(ctx, url)
	if len(raw) == 0 {
		return nil, "", false
	}

	encoded := raw
	mime := http.DetectContentType(raw)
	if img, err := artwork.Decode(raw); err == nil {
		if out, m, err := artwork.Encode(artwork.Resize(img, ytmSearchThumbEdge)); err == nil {
			encoded = out
			mime = m
		}
	}

	p.searchThumbMu.Lock()
	p.searchThumbs.put(ref, encoded, mime)
	p.searchThumbMu.Unlock()
	p.logger().Debug("artwork: search thumb cached in memory", "ref", ref, "bytes", len(encoded))

	return encoded, mime, true
}

func (p *ytmProvider) setOpenCached(ref string, cached bool) {
	if p == nil || ref == "" {
		return
	}
	p.openCachedMu.Lock()
	if p.openCached == nil {
		p.openCached = make(map[string]bool)
	}
	if len(p.openCached) > 64 {
		clear(p.openCached)
	}
	p.openCached[ref] = cached
	p.openCachedMu.Unlock()
}

func (p *ytmProvider) lastOpenCached(ref string) (bool, bool) {
	if p == nil || ref == "" {
		return false, false
	}
	p.openCachedMu.Lock()
	cached, ok := p.openCached[ref]
	p.openCachedMu.Unlock()

	return cached, ok
}
