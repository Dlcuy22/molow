// Navidrome / Subsonic integration for MoLow.
//
// Each Navidrome library stores its non-secret server URL and username in
// library.config (SQLite), and its password inside the unified CredentialStore
// (credentials/<credentialId>.enc, encrypted with AES-256-GCM via the OS keyring).
// navidromeManager implements SourceAdapter, CoverFetcher, LyricFetcher, and
// PlaybackObserver on audioRouter.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molo/core"
	"github.com/dlcuy22/molo/decode"
	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molo/provider"
	"github.com/dlcuy22/molo/stream"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	ndlib "github.com/dlcuy22/molow/internal/library/navidrome"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"github.com/dlcuy22/molow/internal/lyrics"
	nd "github.com/dlcuy22/molow/internal/navidrome"
	"github.com/dlcuy22/molow/internal/trackcache"
)

const (
	navidromeRefPrefix    = "navidrome:"
	navidromeSampleRate   = 48000
	navidromeSyncInterval = 30 * time.Minute
	navidromeMaxTrackSize = 2 << 30 // 2 GiB safety limit matching stream buffers
)

// navidromeLibraryConfig holds non-secret Navidrome server metadata stored in
// SQLite library.config.
type navidromeLibraryConfig struct {
	ServerURL string `json:"serverUrl"`
	Username  string `json:"username"`
}

// navidromeCredentials holds the sensitive Navidrome password encrypted inside
// CredentialStore.
type navidromeCredentials struct {
	Password string `json:"password"`
}

// NavidromeConfigInfo is the non-secret server view exposed to the Edit dialog.
type NavidromeConfigInfo struct {
	LibraryID   string `json:"libraryId"`
	ServerURL   string `json:"serverUrl"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

func isNavidromeRef(ref string) bool {
	return strings.HasPrefix(ref, navidromeRefPrefix)
}

func navidromeStableHash(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "nd_" + hex.EncodeToString(sum[:16])
}

func encodeNavidromeConfig(serverURL, username string) string {
	cfg := navidromeLibraryConfig{
		ServerURL: strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		Username:  strings.TrimSpace(username),
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeNavidromeConfig(raw, fallbackRoot string) navidromeLibraryConfig {
	var cfg navidromeLibraryConfig
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		_ = json.Unmarshal([]byte(trimmed), &cfg)
	}
	if cfg.ServerURL == "" && strings.TrimSpace(fallbackRoot) != "" {
		cfg.ServerURL = strings.TrimRight(strings.TrimSpace(fallbackRoot), "/")
	}
	return cfg
}

// navidromeManager manages all KindNavidrome libraries and implements
// SourceAdapter, CoverFetcher, LyricFetcher, and PlaybackObserver.
type navidromeManager struct {
	svc  *PlayerService
	prov *navidromeProvider

	mu         sync.RWMutex
	configMu   sync.Mutex
	clients    map[string]*nd.Client
	configs    map[string]navidromeLibraryConfig
	syncCancel map[string]context.CancelFunc

	obsMu        sync.Mutex
	activeRef    string
	activeDurMs  int64
	submittedRef string
}

func newNavidromeManager(svc *PlayerService) *navidromeManager {
	var art *artwork.Store
	var tracks *trackcache.Store
	var log *slog.Logger
	if svc != nil {
		art = svc.art
		tracks = svc.tracks
		log = svc.logger()
	}
	m := &navidromeManager{
		svc:        svc,
		clients:    make(map[string]*nd.Client),
		configs:    make(map[string]navidromeLibraryConfig),
		syncCancel: make(map[string]context.CancelFunc),
	}
	m.prov = newNavidromeProvider(art, tracks, log, m)
	return m
}

func (m *navidromeManager) Kind() library.Kind { return library.KindNavidrome }

func (m *navidromeManager) Provider() provider.AudioProvider {
	if m == nil {
		return nil
	}
	return m.prov
}

func (m *navidromeManager) log() *slog.Logger {
	if m != nil && m.svc != nil {
		return m.svc.logger()
	}
	return slog.Default()
}

func (m *navidromeManager) setClient(libraryID string, cfg navidromeLibraryConfig, client *nd.Client) {
	m.mu.Lock()
	m.configs[libraryID] = cfg
	if client != nil {
		m.clients[libraryID] = client
	} else {
		delete(m.clients, libraryID)
	}
	m.mu.Unlock()
}

func (m *navidromeManager) clientForLibrary(libraryID string) (*nd.Client, bool) {
	if m == nil || libraryID == "" {
		return nil, false
	}
	m.mu.RLock()
	c := m.clients[libraryID]
	m.mu.RUnlock()
	return c, c != nil
}

func (m *navidromeManager) clientForRef(ref string) (*nd.Client, string, bool) {
	libID, songID, ok := ndlib.ParseRef(ref)
	if !ok {
		return nil, "", false
	}
	if libID != "" {
		c, exists := m.clientForLibrary(libID)
		return c, songID, exists
	}
	if m.svc != nil {
		if active := m.svc.activeLibrary(); active != "" {
			if c, exists := m.clientForLibrary(active); exists {
				return c, songID, true
			}
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.clients {
		if c != nil {
			return c, songID, true
		}
	}
	return nil, songID, false
}

// Open instantiates a Navidrome source from row, loading its password from
// CredentialStore and scheduling a background sync when due.
func (m *navidromeManager) Open(ctx context.Context, row sqlite.LibraryRow) (library.Source, error) {
	cfg := decodeNavidromeConfig(row.Config, row.Root)
	var client *nd.Client
	status := library.StatusReady
	var statusMsg string

	if cfg.ServerURL == "" || cfg.Username == "" || row.CredentialID == "" {
		status = library.StatusSignedOut
	} else if m.svc != nil {
		cs, err := m.svc.credentials()
		if err != nil {
			status = library.StatusError
			statusMsg = err.Error()
		} else {
			var creds navidromeCredentials
			err := cs.Load(row.CredentialID, library.KindNavidrome, &creds)
			switch {
			case err == nil && creds.Password != "":
				c, cerr := nd.NewClient(nd.Config{
					ServerURL: cfg.ServerURL,
					Username:  cfg.Username,
					Password:  creds.Password,
				}, nil)
				if cerr != nil {
					status = library.StatusError
					statusMsg = cerr.Error()
				} else {
					client = c
				}
			case errors.Is(err, ErrNoCredentials):
				status = library.StatusSignedOut
			case err != nil:
				status = library.StatusError
				statusMsg = err.Error()
				m.log().Warn("navidrome: stored credentials unreadable", "library", row.ID, "error", err)
			default:
				status = library.StatusSignedOut
			}
		}
	}

	m.setClient(row.ID, cfg, client)

	lib := row.Library
	lib.Status = status
	lib.Message = statusMsg

	var art *artwork.Store
	var store *sqlite.Store
	if m.svc != nil {
		art = m.svc.art
		store = m.svc.libStore
	}

	src := ndlib.New(store, lib, art, client, func(p ndlib.SyncProgress) {
		if m.svc != nil {
			m.svc.emit(eventLibrary, LibraryProgress{
				LibraryID: p.LibraryID,
				Done:      p.Done,
				Total:     p.Total,
				State:     "scanning",
			})
			m.svc.emitActivity(ActivityEvent{
				ID:        "navidrome-sync:" + p.LibraryID,
				LibraryID: p.LibraryID,
				Kind:      "sync",
				Title:     firstNonEmpty(lib.Name, "Navidrome"),
				Detail:    "Syncing library",
				Done:      p.Done,
				Total:     p.Total,
				State:     "running",
			})
		}
	})

	if m.svc != nil {
		m.svc.libMu.Lock()
		if m.svc.libs == nil {
			m.svc.libs = make(map[string]library.Source)
		}
		m.svc.libs[row.ID] = src
		m.svc.libMu.Unlock()

		if client != nil && store != nil {
			at, syncErr := store.SyncTime(row.ID, "navidrome")
			if syncErr != nil || at == 0 || time.Since(time.Unix(at, 0)) > navidromeSyncInterval {
				_ = m.svc.triggerNavidromeSync(row.ID, src)
			}
		}
	}
	_ = ctx
	return src, nil
}

func (m *navidromeManager) CloseLibrary(libraryID string) {
	if m == nil || libraryID == "" {
		return
	}
	m.mu.Lock()
	cancel := m.syncCancel[libraryID]
	delete(m.syncCancel, libraryID)
	delete(m.clients, libraryID)
	delete(m.configs, libraryID)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *navidromeManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.syncCancel))
	for id, fn := range m.syncCancel {
		if fn != nil {
			cancels = append(cancels, fn)
		}
		delete(m.syncCancel, id)
	}
	m.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// CoverBytes resolves cover artwork for a Navidrome track ref.
func (m *navidromeManager) CoverBytes(ctx context.Context, ref string, edge int) ([]byte, string, bool) {
	if !isNavidromeRef(ref) || m.svc == nil || m.svc.art == nil {
		return nil, "", false
	}
	edge = artwork.NormalizeThumbEdge(edge)
	if m.svc.libStore != nil {
		if tr, ok, err := m.svc.libStore.TrackByRef(ref); err == nil && ok && tr.CoverID != "" {
			if data, mimeType, found := m.svc.art.ThumbBytes(tr.CoverID, edge); found {
				return data, mimeType, true
			}
		}
	}

	client, songID, ok := m.clientForRef(ref)
	if !ok || client == nil {
		return nil, "", false
	}
	song, err := client.GetSong(ctx, songID)
	if err != nil || song.CoverArt == "" {
		return nil, "", false
	}
	data, _, err := client.GetCoverArt(ctx, song.CoverArt, 512)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	cid, err := m.svc.art.Put(data)
	if err != nil || cid == "" {
		return nil, "", false
	}
	return m.svc.art.ThumbBytes(cid, edge)
}

// FetchLyrics fetches lyrics from the owning Navidrome server when local
// sidecars miss.
func (m *navidromeManager) FetchLyrics(ctx context.Context, ref string) (*lyrics.Document, error) {
	if !isNavidromeRef(ref) || m.svc == nil {
		return nil, nil
	}
	client, songID, ok := m.clientForRef(ref)
	if !ok || client == nil {
		return nil, nil
	}
	var artist, title string
	if m.svc.libStore != nil {
		if tr, found, err := m.svc.libStore.TrackByRef(ref); err == nil && found {
			artist = tr.Artist
			title = tr.Title
		}
	}
	if title == "" {
		if song, err := client.GetSong(ctx, songID); err == nil {
			artist = song.Artist
			title = song.Title
		}
	}
	if title == "" {
		return nil, nil
	}
	text, _, err := client.GetLyrics(ctx, songID, artist, title)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	lines, synced, err := lyrics.Parse(text)
	if err != nil || len(lines) == 0 {
		return nil, nil
	}
	return &lyrics.Document{
		Lines:    lines,
		Synced:   synced,
		Provider: "navidrome",
	}, nil
}

// OnTrackStart records track start and submits a now-playing announcement.
func (m *navidromeManager) OnTrackStart(ctx context.Context, ref string, durationMs int64) {
	if !isNavidromeRef(ref) {
		return
	}
	client, songID, ok := m.clientForRef(ref)
	if !ok || client == nil {
		return
	}

	m.obsMu.Lock()
	m.submittedRef = ""
	m.activeRef = ref
	m.activeDurMs = durationMs
	m.obsMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	scrobbleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = client.Scrobble(scrobbleCtx, songID, false)
}

// OnTrackEnd submits a scrobble when playback crosses Navidrome's 50% / 4-minute
// threshold.
func (m *navidromeManager) OnTrackEnd(ctx context.Context, ref string, playedMs int64) {
	if !isNavidromeRef(ref) {
		return
	}
	client, songID, ok := m.clientForRef(ref)
	if !ok || client == nil {
		return
	}

	m.obsMu.Lock()
	if m.submittedRef == ref {
		m.obsMu.Unlock()
		return
	}
	var durMs int64
	if m.activeRef == ref {
		durMs = m.activeDurMs
		m.activeRef = ""
		m.activeDurMs = 0
	}
	if durMs <= 0 && m.svc != nil && m.svc.libStore != nil {
		if tr, found, err := m.svc.libStore.TrackByRef(ref); err == nil && found {
			durMs = tr.DurationMs
		}
	}
	eligible := false
	if durMs > 0 && playedMs > 0 {
		if playedMs >= durMs/2 || playedMs >= 4*60*1000 {
			eligible = true
		}
	} else if durMs <= 0 && playedMs >= 30*1000 {
		eligible = true
	}
	if !eligible {
		m.obsMu.Unlock()
		return
	}
	m.submittedRef = ref
	m.obsMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	scrobbleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = client.Scrobble(scrobbleCtx, songID, true)
}

// navidromeProvider resolves Navidrome track refs into progressive audio streams.
type navidromeProvider struct {
	art    *artwork.Store
	tracks *trackcache.Store
	log    *slog.Logger
	mgr    *navidromeManager
}

func newNavidromeProvider(art *artwork.Store, tracks *trackcache.Store, log *slog.Logger, mgr *navidromeManager) *navidromeProvider {
	if log == nil {
		log = slog.Default()
	}
	return &navidromeProvider{
		art:    art,
		tracks: tracks,
		log:    log,
		mgr:    mgr,
	}
}

func (p *navidromeProvider) Name() string { return "navidrome" }

func (p *navidromeProvider) Match(ref string) bool { return isNavidromeRef(ref) }

func (p *navidromeProvider) catalogTrack(ref string) (library.Track, string, bool) {
	if p.mgr == nil || p.mgr.svc == nil || p.mgr.svc.libStore == nil {
		return library.Track{}, "", false
	}
	tr, ok, err := p.mgr.svc.libStore.TrackByRef(ref)
	if err != nil || !ok {
		return library.Track{}, "", false
	}
	return tr, tr.NativeID, true
}

func (p *navidromeProvider) Open(ctx context.Context, ref string) (provider.Source, error) {
	if err := ctx.Err(); err != nil {
		return provider.Source{}, err
	}
	libID, songID, ok := ndlib.ParseRef(ref)
	if !ok {
		return provider.Source{}, fmt.Errorf("navidrome: invalid ref %q", ref)
	}
	var client *nd.Client
	var hasClient bool
	if p.mgr != nil {
		client, _, hasClient = p.mgr.clientForRef(ref)
	}

	tr, _, hasMeta := p.catalogTrack(ref)
	var suffix string
	var coverRef string
	if !hasMeta && hasClient {
		if song, err := client.GetSong(ctx, songID); err == nil {
			tr = library.Track{
				Ref:        ref,
				LibraryID:  libID,
				NativeID:   song.ID,
				Title:      song.Title,
				Artist:     song.Artist,
				Album:      song.Album,
				DurationMs: int64(song.Duration) * 1000,
			}
			coverRef = song.CoverArt
			suffix = song.Suffix
			hasMeta = true
		}
	}

	var coverBytes []byte
	var coverMIME string
	if tr.CoverID != "" && p.art != nil {
		if data, mimeType, found := p.art.Bytes(tr.CoverID); found {
			coverBytes = data
			coverMIME = mimeType
		}
	}
	if len(coverBytes) == 0 && hasClient && coverRef != "" {
		if data, mimeType, err := client.GetCoverArt(ctx, coverRef, 512); err == nil && len(data) > 0 {
			coverBytes = data
			coverMIME = mimeType
			if p.art != nil {
				_, _ = p.art.Put(data)
			}
		}
	}
	if len(coverBytes) > 0 && coverMIME == "" {
		coverMIME = http.DetectContentType(coverBytes)
	}

	src := provider.Source{
		Meta: meta.Meta{
			Tags: meta.Tags{
				Title:     tr.Title,
				Artist:    tr.Artist,
				Album:     tr.Album,
				Cover:     coverBytes,
				CoverMIME: coverMIME,
			},
		},
	}

	if tr.DurationMs > 0 {
		frames := (tr.DurationMs * navidromeSampleRate) / 1000
		src.Meta.Stream = core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: frames}
		src.Probe = func(core.DurationMode) (core.StreamInfo, error) {
			return core.StreamInfo{Format: core.CanonicalFormat, TotalFrames: frames}, nil
		}
	}

	cacheKey := navidromeStableHash(ref)
	if p.tracks != nil && p.tracks.Has(cacheKey) {
		src.Opener = func(stop <-chan struct{}) (decode.Decoder, error) {
			if rc, _, ok := p.tracks.Get(cacheKey); ok {
				dec, err := openSeekableContainer(rc)
				if err != nil {
					_ = rc.Close()
					return nil, err
				}
				return wrapClosingDecoder(dec, rc), nil
			}
			if !hasClient {
				return nil, fmt.Errorf("navidrome: server disconnected")
			}
			return p.downloadAndOpen(stop, client, songID, cacheKey, suffix)
		}
		return src, nil
	}

	if !hasClient {
		return provider.Source{}, fmt.Errorf("navidrome: server is not connected")
	}

	var (
		mu   sync.Mutex
		buf  *ytmStreamBuffer
		init bool
	)
	start := func(stop <-chan struct{}) (*ytmStreamBuffer, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if init {
			if buf == nil {
				return nil, "", errors.New("navidrome: stream failed to initialize")
			}
			return buf, buf.ext, nil
		}

		dlCtx, cancel := context.WithCancel(context.Background())
		if stop != nil {
			go func() {
				select {
				case <-stop:
					cancel()
				case <-dlCtx.Done():
				}
			}()
		}

		resp, err := client.OpenStream(dlCtx, songID, nil)
		if err != nil {
			cancel()
			return nil, "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			cancel()
			return nil, "", fmt.Errorf("navidrome: stream returned HTTP %d", resp.StatusCode)
		}

		ext := extFromStream(suffix, resp.Header.Get("Content-Type"))
		b := newYTMStreamBuffer(cacheKey, p.tracks, p.log)
		b.ext = ext
		if err := b.initFile(); err != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, "", err
		}

		buf = b
		init = true

		go func() {
			defer cancel()
			defer resp.Body.Close()
			copyErr := copyStreamToBuffer(dlCtx, resp.Body, b, navidromeMaxTrackSize)
			b.finish(copyErr)
		}()

		return b, ext, nil
	}

	src.Opener = func(stop <-chan struct{}) (decode.Decoder, error) {
		if p.tracks != nil {
			if rc, _, ok := p.tracks.Get(cacheKey); ok {
				dec, err := openSeekableContainer(rc)
				if err == nil {
					return wrapClosingDecoder(dec, rc), nil
				}
				_ = rc.Close()
			}
		}

		b, _, err := start(stop)
		if err != nil {
			return nil, err
		}

		r, err := b.reader()
		if err != nil {
			return nil, err
		}
		dec, err := decode.Default.OpenReader(r)
		if err == nil {
			b.mu.Lock()
			b.verified = true
			b.mu.Unlock()
			return wrapClosingDecoder(dec, r), nil
		}
		_ = r.Close()
		if !errors.Is(err, decode.ErrUnsupported) {
			return nil, err
		}

		if err := b.waitStop(context.Background(), stop); err != nil {
			return nil, err
		}
		return b.openSeekableDecoder()
	}

	src.Upgrade = func(uctx context.Context) (stream.Opener, error) {
		b, _, err := start(nil)
		if err != nil {
			return nil, err
		}
		if err := b.wait(uctx); err != nil {
			return nil, err
		}
		return func(<-chan struct{}) (decode.Decoder, error) {
			return b.openSeekableDecoder()
		}, nil
	}

	return src, nil
}

func (p *navidromeProvider) downloadAndOpen(stop <-chan struct{}, client *nd.Client, songID, cacheKey, suffix string) (decode.Decoder, error) {
	dlCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stop != nil {
		go func() {
			select {
			case <-stop:
				cancel()
			case <-dlCtx.Done():
			}
		}()
	}

	resp, err := client.OpenStream(dlCtx, songID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("navidrome: stream returned HTTP %d", resp.StatusCode)
	}

	ext := extFromStream(suffix, resp.Header.Get("Content-Type"))
	b := newYTMStreamBuffer(cacheKey, p.tracks, p.log)
	b.ext = ext
	if err := b.initFile(); err != nil {
		return nil, err
	}

	copyErr := copyStreamToBuffer(dlCtx, resp.Body, b, navidromeMaxTrackSize)
	b.finish(copyErr)
	if copyErr != nil {
		return nil, copyErr
	}

	return b.openSeekableDecoder()
}

func copyStreamToBuffer(ctx context.Context, body io.Reader, b *ytmStreamBuffer, limit int64) error {
	chunk := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := body.Read(chunk)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return fmt.Errorf("navidrome: stream exceeds %d bytes", limit)
			}
			if werr := b.append(chunk[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func extFromStream(suffix, contentType string) string {
	s := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(suffix), "."))
	switch s {
	case "flac", "mp3", "m4a", "mp4", "opus", "ogg", "oga", "wav", "aac", "webm":
		return "." + s
	}
	if contentType != "" {
		mt, _, _ := mime.ParseMediaType(contentType)
		switch mt {
		case "audio/flac", "audio/x-flac":
			return ".flac"
		case "audio/mpeg", "audio/mp3":
			return ".mp3"
		case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/aac":
			return ".m4a"
		case "audio/ogg":
			return ".ogg"
		case "audio/opus":
			return ".opus"
		case "audio/wav", "audio/x-wav", "audio/wave":
			return ".wav"
		case "audio/webm":
			return ".webm"
		}
	}
	return ".mp3"
}

// TestNavidromeConnection verifies that serverURL, username, and password can
// authenticate against a Subsonic/Navidrome server.
func (s *PlayerService) TestNavidromeConnection(serverURL, username, password string) error {
	client, err := nd.NewClient(nd.Config{
		ServerURL: serverURL,
		Username:  username,
		Password:  password,
	}, nil)
	if err != nil {
		return err
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	_, err = client.Ping(ctx)
	return err
}

// AddNavidromeLibrary tests the connection, encrypts the password into
// CredentialStore, registers the library row, and starts an initial sync.
func (s *PlayerService) AddNavidromeLibrary(name, description, serverURL, username, password string) (library.Library, error) {
	name, description, err := validateLibraryFields(name, description)
	if err != nil {
		return library.Library{}, err
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	username = strings.TrimSpace(username)
	if serverURL == "" || username == "" || password == "" {
		return library.Library{}, fmt.Errorf("navidrome: server URL, username, and password are required")
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}

	cs, err := s.credentials()
	if err != nil {
		return library.Library{}, err
	}
	if err := cs.ks.Probe(); err != nil {
		return library.Library{}, err
	}

	if err := s.TestNavidromeConnection(serverURL, username, password); err != nil {
		return library.Library{}, err
	}

	mgr := s.navidromeManager()
	mgr.configMu.Lock()
	defer mgr.configMu.Unlock()

	libID, err := newRemoteLibraryID(library.KindNavidrome)
	if err != nil {
		return library.Library{}, err
	}
	credID, err := newCredentialID()
	if err != nil {
		return library.Library{}, err
	}

	if err := cs.Save(credID, library.KindNavidrome, navidromeCredentials{Password: password}); err != nil {
		return library.Library{}, err
	}

	row, err := store.AddLibraryRow(sqlite.LibraryRow{
		Library: library.Library{
			ID:          libID,
			Kind:        library.KindNavidrome,
			Name:        name,
			Description: description,
			Root:        serverURL,
			Status:      library.StatusReady,
		},
		CredentialID: credID,
		Config:       encodeNavidromeConfig(serverURL, username),
	})
	if err != nil {
		_ = cs.Delete(credID)
		return library.Library{}, err
	}

	s.ensureSourceRouter()
	src, err := s.navidromeManager().Open(s.ctx, row)
	if err != nil {
		return library.Library{}, err
	}
	if ndSrc, ok := src.(*ndlib.Source); ok {
		_ = s.triggerNavidromeSync(libID, ndSrc)
	}
	return s.describeLibrary(libID)
}

// GetNavidromeConfig returns the non-secret server URL and username for an
// existing Navidrome library so the edit dialog can prefill them.
func (s *PlayerService) GetNavidromeConfig(id string) (NavidromeConfigInfo, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return NavidromeConfigInfo{}, fmt.Errorf("library: store is not available")
	}
	row, err := store.LibraryRow(id)
	if err != nil {
		return NavidromeConfigInfo{}, err
	}
	if row.Kind != library.KindNavidrome {
		return NavidromeConfigInfo{}, fmt.Errorf("library: %q is not a Navidrome library", id)
	}
	cfg := decodeNavidromeConfig(row.Config, row.Root)
	return NavidromeConfigInfo{
		LibraryID:   row.ID,
		ServerURL:   cfg.ServerURL,
		Username:    cfg.Username,
		HasPassword: row.CredentialID != "",
	}, nil
}

// UpdateNavidromeLibrary updates metadata and/or server credentials for an
// existing Navidrome library. An empty password preserves the stored password.
func (s *PlayerService) UpdateNavidromeLibrary(id, name, description, serverURL, username, password string) (library.Library, error) {
	name, description, err := validateLibraryFields(name, description)
	if err != nil {
		return library.Library{}, err
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	username = strings.TrimSpace(username)
	if serverURL == "" || username == "" {
		return library.Library{}, fmt.Errorf("navidrome: server URL and username are required")
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}
	row, err := store.LibraryRow(id)
	if err != nil {
		return library.Library{}, err
	}
	if row.Kind != library.KindNavidrome {
		return library.Library{}, fmt.Errorf("library: %q is not a Navidrome library", id)
	}

	cs, err := s.credentials()
	if err != nil {
		return library.Library{}, err
	}

	effectivePassword := password
	if effectivePassword == "" {
		if row.CredentialID == "" {
			return library.Library{}, fmt.Errorf("navidrome: password is required")
		}
		var existing navidromeCredentials
		if err := cs.Load(row.CredentialID, library.KindNavidrome, &existing); err != nil {
			return library.Library{}, err
		}
		effectivePassword = existing.Password
	}

	if err := s.TestNavidromeConnection(serverURL, username, effectivePassword); err != nil {
		return library.Library{}, err
	}

	mgr := s.navidromeManager()
	mgr.configMu.Lock()
	defer mgr.configMu.Unlock()

	client, err := nd.NewClient(nd.Config{
		ServerURL: serverURL,
		Username:  username,
		Password:  effectivePassword,
	}, nil)
	if err != nil {
		return library.Library{}, err
	}

	oldCredID := row.CredentialID
	newCredID := oldCredID
	if password != "" {
		if err := cs.ks.Probe(); err != nil {
			return library.Library{}, err
		}
		newCredID, err = newCredentialID()
		if err != nil {
			return library.Library{}, err
		}
		if err := cs.Save(newCredID, library.KindNavidrome, navidromeCredentials{Password: password}); err != nil {
			return library.Library{}, err
		}
	}

	rollbackCred := func() {
		if password != "" && newCredID != "" && newCredID != oldCredID {
			_ = cs.Delete(newCredID)
		}
	}

	if _, err := store.UpdateLibrary(id, name, description); err != nil {
		rollbackCred()
		return library.Library{}, err
	}
	if err := store.UpdateLibraryConfig(id, encodeNavidromeConfig(serverURL, username)); err != nil {
		_, _ = store.UpdateLibrary(id, row.Name, row.Description)
		rollbackCred()
		return library.Library{}, err
	}
	if password != "" {
		if err := store.UpdateLibraryCredential(id, newCredID); err != nil {
			_ = store.UpdateLibraryConfig(id, row.Config)
			_, _ = store.UpdateLibrary(id, row.Name, row.Description)
			rollbackCred()
			return library.Library{}, err
		}
		if oldCredID != "" && oldCredID != newCredID {
			_ = cs.Delete(oldCredID)
		}
	}

	mgr.setClient(id, navidromeLibraryConfig{ServerURL: serverURL, Username: username}, client)
	if src, ok := s.librarySource(id); ok {
		if ndSrc, isND := src.(*ndlib.Source); isND {
			ndSrc.SetClient(client)
			_ = s.triggerNavidromeSync(id, ndSrc)
		}
	}

	return s.describeLibrary(id)
}

func (s *PlayerService) triggerNavidromeSync(libraryID string, src *ndlib.Source) error {
	if src == nil {
		return fmt.Errorf("navidrome: source not open")
	}
	s.libMu.Lock()
	if s.scanning == nil {
		s.scanning = make(map[string]bool)
	}
	if s.scanning[libraryID] {
		s.libMu.Unlock()
		return nil
	}
	s.scanning[libraryID] = true
	s.libMu.Unlock()

	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	syncCtx, cancel := context.WithCancel(parent)
	mgr := s.navidromeManager()
	mgr.mu.Lock()
	if prev := mgr.syncCancel[libraryID]; prev != nil {
		prev()
	}
	mgr.syncCancel[libraryID] = cancel
	mgr.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.libMu.Lock()
			delete(s.scanning, libraryID)
			s.libMu.Unlock()
		}()

		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "scanning"})
		n, err := src.Sync(syncCtx)
		if err != nil {
			s.logger().Warn("navidrome: sync failed", "library", libraryID, "error", err)
			s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "error"})
			s.emitActivity(ActivityEvent{
				ID:        "navidrome-sync:" + libraryID,
				LibraryID: libraryID,
				Kind:      "sync",
				Title:     "Navidrome",
				Detail:    err.Error(),
				State:     "error",
			})
			return
		}
		s.refreshLibraryCover(libraryID)
		s.libMu.Lock()
		st := s.libStore
		s.libMu.Unlock()
		if st != nil {
			_ = st.SetSyncTime(libraryID, "navidrome", time.Now().Unix())
		}
		if lists, lerr := src.Playlists(syncCtx); lerr == nil {
			for _, pl := range lists {
				if !pl.CustomCover {
					s.refreshPlaylistCover(pl.ID)
				}
			}
		}
		s.logger().Info("navidrome: sync complete", "library", libraryID, "tracks", n)
		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: n, Total: n, State: "ready"})
		s.emitActivity(ActivityEvent{
			ID:        "navidrome-sync:" + libraryID,
			LibraryID: libraryID,
			Kind:      "sync",
			Title:     "Navidrome",
			Detail:    fmt.Sprintf("Synced %d tracks", n),
			Done:      n,
			Total:     n,
			State:     "done",
		})
	}()

	return nil
}

// catalogNavidromeTrack fetches a catalogued Navidrome track from SQLite for
// queue row enrichment.
func (s *PlayerService) catalogNavidromeTrack(ref string) (library.Track, bool) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || !isNavidromeRef(ref) {
		return library.Track{}, false
	}
	tr, ok, err := store.TrackByRef(ref)
	if err != nil || !ok {
		return library.Track{}, false
	}
	return tr, true
}
