package backend

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molo/provider"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/local"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"github.com/dlcuy22/molow/internal/lyrics"
)

// SourceAdapter plugs a library backend into PlayerService, providing runtime
// library instantiation, audio stream resolution, and clean shutdown.
type SourceAdapter interface {
	Kind() library.Kind
	Open(ctx context.Context, row sqlite.LibraryRow) (library.Source, error)
	Provider() provider.AudioProvider
	CloseLibrary(libraryID string)
	Close()
}

// CoverFetcher is an optional SourceAdapter seam that resolves image bytes for
// a catalogued or queued remote reference.
type CoverFetcher interface {
	CoverBytes(ctx context.Context, ref string, edge int) ([]byte, string, bool)
}

// LyricFetcher is an optional SourceAdapter seam that supplies source-hosted
// lyrics when local sidecars and web providers miss.
type LyricFetcher interface {
	FetchLyrics(ctx context.Context, ref string) (*lyrics.Document, error)
}

// PlaybackObserver is an optional SourceAdapter seam for remote servers that
// track now-playing announcements or scrobbles.
type PlaybackObserver interface {
	OnTrackStart(ctx context.Context, ref string, durationMs int64)
	OnTrackEnd(ctx context.Context, ref string, playedMs int64)
}

// audioRouter implements provider.AudioProvider and routes playback, cover,
// lyric, and lifecycle hooks across registered SourceAdapters.
type audioRouter struct {
	mu       sync.RWMutex
	adapters []SourceAdapter
	byKind   map[library.Kind]SourceAdapter
}

func newAudioRouter() *audioRouter {
	return &audioRouter{
		byKind: make(map[library.Kind]SourceAdapter),
	}
}

func (r *audioRouter) Register(adapter SourceAdapter) {
	if r == nil || adapter == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := adapter.Kind()
	if _, exists := r.byKind[kind]; exists {
		for i, existing := range r.adapters {
			if existing.Kind() == kind {
				r.adapters[i] = adapter
				break
			}
		}
	} else {
		r.adapters = append(r.adapters, adapter)
	}
	r.byKind[kind] = adapter
}

func (r *audioRouter) Adapter(kind library.Kind) (SourceAdapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byKind[kind]
	return a, ok
}

func (r *audioRouter) Name() string { return "router" }

func (r *audioRouter) Match(ref string) bool {
	return r.providerFor(ref) != nil
}

func (r *audioRouter) Open(ctx context.Context, ref string) (provider.Source, error) {
	p := r.providerFor(ref)
	if p == nil {
		return provider.Source{}, provider.ErrNoProvider
	}
	return p.Open(ctx, ref)
}

func (r *audioRouter) providerFor(ref string) provider.AudioProvider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.adapters {
		if p := a.Provider(); p != nil && p.Match(ref) {
			return p
		}
	}
	return nil
}

func (r *audioRouter) matchingAdapters(ref string) []SourceAdapter {
	if r == nil || ref == "" {
		return nil
	}
	kind, _, _, isRemote := library.ParseRemoteRef(ref)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []SourceAdapter
	if isRemote {
		if a, ok := r.byKind[kind]; ok {
			return []SourceAdapter{a}
		}
	}
	for _, a := range r.adapters {
		if p := a.Provider(); p != nil && p.Match(ref) {
			out = append(out, a)
		}
	}
	return out
}

func (r *audioRouter) CoverBytes(ctx context.Context, ref string, edge int) ([]byte, string, bool) {
	for _, a := range r.matchingAdapters(ref) {
		if cf, ok := a.(CoverFetcher); ok {
			if data, mime, found := cf.CoverBytes(ctx, ref, edge); found {
				return data, mime, true
			}
		}
	}
	return nil, "", false
}

func (r *audioRouter) FetchLyrics(ctx context.Context, ref string) (*lyrics.Document, error) {
	for _, a := range r.matchingAdapters(ref) {
		if lf, ok := a.(LyricFetcher); ok {
			if doc, err := lf.FetchLyrics(ctx, ref); err == nil && doc != nil {
				return doc, nil
			} else if err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

func (r *audioRouter) hasPlaybackObserver(ref string) bool {
	for _, a := range r.matchingAdapters(ref) {
		if _, ok := a.(PlaybackObserver); ok {
			return true
		}
	}
	return false
}

func (r *audioRouter) NotifyTrackStart(ctx context.Context, ref string, durationMs int64) {
	for _, a := range r.matchingAdapters(ref) {
		if obs, ok := a.(PlaybackObserver); ok {
			obs.OnTrackStart(ctx, ref, durationMs)
		}
	}
}

func (r *audioRouter) NotifyTrackEnd(ctx context.Context, ref string, playedMs int64) {
	for _, a := range r.matchingAdapters(ref) {
		if obs, ok := a.(PlaybackObserver); ok {
			obs.OnTrackEnd(ctx, ref, playedMs)
		}
	}
}

func (r *audioRouter) Close() {
	if r == nil {
		return
	}
	r.mu.RLock()
	adapters := append([]SourceAdapter(nil), r.adapters...)
	r.mu.RUnlock()
	for _, a := range adapters {
		a.Close()
	}
}

// localAdapter implements SourceAdapter for local filesystem libraries.
type localAdapter struct {
	svc *PlayerService
}

func newLocalAdapter(svc *PlayerService) *localAdapter {
	return &localAdapter{svc: svc}
}

func (a *localAdapter) Kind() library.Kind { return library.KindLocal }

func (a *localAdapter) Provider() provider.AudioProvider { return nil }

func (a *localAdapter) Open(_ context.Context, row sqlite.LibraryRow) (library.Source, error) {
	s := a.svc
	if s == nil {
		return nil, nil
	}
	src := s.registerLocal(row.Library)
	if s.libStore == nil {
		return src, nil
	}
	roots, err := s.libStore.Roots(row.ID)
	if err != nil {
		s.logger().Warn("library: roots", "id", row.ID, "error", err)
		return src, nil
	}
	for _, root := range roots {
		_ = s.libStore.LinkLegacyFolderPlaylist(row.ID, root, strings.TrimSpace(filepathBase(root)))
	}
	s.syncWatchedFolderRoots(row.ID)
	s.registerLocalSyncer(row.ID)

	if watchedRoots, err := s.libStore.WatchedRoots(row.ID); err == nil && len(watchedRoots) > 0 {
		_ = s.scanLibrary(row.ID, "")
	}
	if hasRemote, err := s.libStore.HasWatchedRemotePlaylists(row.ID); err == nil && hasRemote {
		at, syncErr := s.libStore.SyncTime(row.ID, "local_remote_playlists")
		if syncErr != nil || at == 0 || time.Since(time.Unix(at, 0)) > ytmOpenStaleAge {
			s.triggerLocalRemoteSync(row.ID, syncTriggerOpen)
		}
	}
	return src, nil
}

func (a *localAdapter) CloseLibrary(libraryID string) {
	s := a.svc
	if s == nil {
		return
	}
	s.libMu.Lock()
	localSyn := s.localSyncers[libraryID]
	delete(s.localSyncers, libraryID)
	watcher := s.localWatcher
	s.libMu.Unlock()
	if localSyn != nil {
		localSyn.stop()
	}
	if watcher != nil {
		watcher.RemoveLibrary(libraryID)
	}
}

func (a *localAdapter) Close() {
	if a.svc != nil {
		a.svc.stopLocalSyncers()
	}
}

func filepathBase(p string) string {
	idx := strings.LastIndexAny(p, `/\`)
	if idx < 0 {
		return p
	}
	if idx == len(p)-1 {
		return ""
	}
	return p[idx+1:]
}

// playbackTracker watches snapshot track transitions and forwards start/end
// callbacks to any registered PlaybackObserver without blocking the pump.
type playbackTracker struct {
	mu        sync.Mutex
	lastRef   string
	startedAt time.Time
}

func (s *PlayerService) syncPlaybackObserver(snap Snapshot) {
	if s.sources == nil {
		return
	}
	activeRef := snap.Path
	if snap.State == "stopped" {
		activeRef = ""
	}

	s.playbackObs.mu.Lock()
	prevRef := s.playbackObs.lastRef
	startedAt := s.playbackObs.startedAt
	if activeRef == prevRef {
		s.playbackObs.mu.Unlock()
		return
	}
	s.playbackObs.lastRef = activeRef
	if activeRef != "" {
		s.playbackObs.startedAt = time.Now()
	} else {
		s.playbackObs.startedAt = time.Time{}
	}
	s.playbackObs.mu.Unlock()

	if prevRef != "" && s.sources.hasPlaybackObserver(prevRef) {
		playedMs := int64(0)
		if !startedAt.IsZero() {
			playedMs = time.Since(startedAt).Milliseconds()
		}
		go s.sources.NotifyTrackEnd(context.Background(), prevRef, playedMs)
	}
	if activeRef != "" && s.sources.hasPlaybackObserver(activeRef) {
		parent := s.ctx
		if parent == nil {
			parent = context.Background()
		}
		go s.sources.NotifyTrackStart(parent, activeRef, snap.Duration)
	}
}

// Ensure *local.Local satisfies library.Source cleanly in backend package checks.
var _ library.Source = (*local.Local)(nil)
