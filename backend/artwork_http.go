package backend

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	artRoutePrefix       = "/art/"
	artAvatarPrefix      = "avatar/"
	artBackdropPrefix    = "bg/"
	artYTMSearchPrefix   = "ytm-search/"
	artImmutableCacheHdr = "public, max-age=31536000, immutable"
	artVolatileCacheHdr  = "public, max-age=3600"
)

// artworkMiddleware intercepts /art/ requests ahead of Wails' asset handler and
// Vite dev proxy so covers stream as raw JPEG/PNG over the WebView's own asset
// server instead of crossing JSON-RPC as base64 strings.
func (s *PlayerService) artworkMiddleware() application.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, artRoutePrefix) {
				next.ServeHTTP(w, r)

				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

				return
			}
			s.serveArtwork(w, r)
		})
	}
}

func (s *PlayerService) serveArtwork(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, artRoutePrefix)
	if rest == "" {
		s.logger().Debug("artwork: miss", "path", r.URL.Path)
		http.NotFound(w, r)

		return
	}
	edge := parseArtworkEdge(r)

	if strings.HasPrefix(rest, artAvatarPrefix) {
		artistID := strings.TrimSpace(strings.TrimPrefix(rest, artAvatarPrefix))
		if artistID == "" || strings.ContainsAny(artistID, `/\`) {
			s.logger().Debug("artwork: miss", "path", r.URL.Path)
			http.NotFound(w, r)

			return
		}
		data, mime, ok := s.ytmProv.artistAvatarBytesForEdge(r.Context(), artistID, edge)
		if !ok {
			s.logger().Debug("artwork: miss", "path", r.URL.Path)
			http.NotFound(w, r)

			return
		}
		writeImageBytes(w, r, data, mime, artVolatileCacheHdr)

		return
	}

	if strings.HasPrefix(rest, artYTMSearchPrefix) {
		videoID := strings.TrimSpace(strings.TrimPrefix(rest, artYTMSearchPrefix))
		if videoID == "" || strings.ContainsAny(videoID, `/\`) {
			s.logger().Debug("artwork: miss", "path", r.URL.Path)
			http.NotFound(w, r)

			return
		}
		data, mime, ok := s.ytmSearchCoverBytes(r.Context(), ytmRef(videoID), edge)
		if !ok {
			s.logger().Debug("artwork: miss", "path", r.URL.Path)
			http.NotFound(w, r)

			return
		}
		writeImageBytes(w, r, data, mime, artVolatileCacheHdr)

		return
	}

	if strings.HasPrefix(rest, artBackdropPrefix) {
		s.serveBackdrop(w, r, strings.TrimPrefix(rest, artBackdropPrefix))

		return
	}

	// Content-addressed artwork id from artwork.Store. Reject path separators
	// and dots before touching disk.
	id := rest
	if strings.ContainsAny(id, `/\.`) {
		s.logger().Debug("artwork: miss", "path", r.URL.Path)
		http.NotFound(w, r)

		return
	}
	if s.art == nil {
		s.logger().Debug("artwork: miss", "path", r.URL.Path)
		http.NotFound(w, r)

		return
	}

	if s.serveCoverPath(w, r, id, edge) {
		return
	}

	// Fallback 1: the active playing track carries embedded cover bytes that
	// have not been normalized onto disk yet (e.g. an unscanned dropped file).
	if s.player != nil {
		data := s.player.Snapshot().Meta.Tags.Cover
		if artwork.ID(data) == id {
			if _, err := s.art.Put(data); err == nil {
				if s.serveCoverPath(w, r, id, edge) {
					return
				}
			}
		}
	}

	// Fallback 2: a queued row passed its track path so its embedded or remote
	// cover can be materialized on first view.
	if trackPath := r.URL.Query().Get("path"); trackPath != "" {
		if _, _, _, isRemote := library.ParseRemoteRef(trackPath); isRemote {
			if data, mime, ok := s.coverBytesForRemoteRef(r.Context(), trackPath, edge); ok {
				writeImageBytes(w, r, data, mime, artImmutableCacheHdr)

				return
			}
		} else if path, ok := s.materializeQueueCover(r.Context(), id, trackPath, edge); ok {
			w.Header().Set("Cache-Control", artImmutableCacheHdr)
			http.ServeFile(w, r, path)

			return
		}
	}

	s.logger().Debug("artwork: miss", "path", r.URL.Path)
	http.NotFound(w, r)
}

func (s *PlayerService) serveCoverPath(w http.ResponseWriter, r *http.Request, id string, edge int) bool {
	path, err := s.art.ThumbPath(id, edge)
	if err != nil || path == "" {
		return false
	}
	w.Header().Set("Cache-Control", artImmutableCacheHdr)
	http.ServeFile(w, r, path)

	return true
}

func parseArtworkEdge(r *http.Request) int {
	if r == nil {
		return artwork.MaxEdge
	}
	px, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("s")))
	if err != nil || px <= 0 {
		return artwork.MaxEdge
	}

	return artwork.NormalizeThumbEdge(px)
}

func (s *PlayerService) serveBackdrop(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" || strings.ContainsAny(id, `/\.`) || s.art == nil {
		s.logger().Debug("artwork: miss", "path", r.URL.Path)
		http.NotFound(w, r)

		return
	}

	if path, err := s.art.BackdropPath(id); err == nil && path != "" {
		w.Header().Set("Cache-Control", artImmutableCacheHdr)
		http.ServeFile(w, r, path)

		return
	}

	if s.player != nil {
		data := s.player.Snapshot().Meta.Tags.Cover
		if artwork.ID(data) == id {
			if _, err := s.art.Put(data); err == nil {
				if path, err := s.art.BackdropPath(id); err == nil && path != "" {
					w.Header().Set("Cache-Control", artImmutableCacheHdr)
					http.ServeFile(w, r, path)

					return
				}
			}
		}
	}

	s.logger().Debug("artwork: miss", "path", r.URL.Path)
	http.NotFound(w, r)
}

func (s *PlayerService) thumbPath(id string, edge int) (string, bool) {
	path, err := s.art.ThumbPath(id, edge)
	if err != nil || path == "" {
		return "", false
	}

	return path, true
}

func (s *PlayerService) coverBytesForRemoteRef(ctx context.Context, trackPath string, edge int) ([]byte, string, bool) {
	if s.sources != nil {
		if data, mime, ok := s.sources.CoverBytes(ctx, trackPath, edge); ok {
			return data, mime, true
		}
	}
	if isYTMRef(trackPath) && s.ytmMgr == nil && s.ytmProv != nil {
		return newYTMManager(s, s.ytmProv).CoverBytes(ctx, trackPath, edge)
	}

	return nil, "", false
}

func (s *PlayerService) materializeQueueCover(ctx context.Context, id, trackPath string, edge int) (string, bool) {
	tags := s.index.lookup(trackPath)
	if tags.CoverID != id {
		return "", false
	}
	m, err := meta.Resolve(ctx, trackPath)
	if err != nil || m == nil {
		return "", false
	}
	data := m.Tags.Cover
	if artwork.ID(data) != id {
		return "", false
	}
	if _, err := s.art.Put(data); err != nil {
		return "", false
	}

	return s.thumbPath(id, edge)
}

func (s *PlayerService) ytmSearchCoverBytes(ctx context.Context, ref string, edge int) ([]byte, string, bool) {
	t, ok := s.ytm.lookup(ref)
	if !ok {
		if cat, found := s.catalogYTMTrack(ref); found {
			s.ytm.put(cat)
			t, ok = cat, true
		}
	}
	if !ok {
		return nil, "", false
	}
	edge = artwork.NormalizeThumbEdge(edge)
	// Reuse already-stored canonical art for tracks in the library catalogue
	// without triggering a network request or a new disk write.
	if s.art != nil && t.CoverID != "" {
		if data, mime, ok := s.art.ThumbBytes(t.CoverID, edge); ok {
			return data, mime, true
		}
	}

	data, mime, ok := s.ytmProv.searchThumbBytes(ctx, ref, t.ThumbnailURL)
	if !ok {
		return nil, "", false
	}
	data, mime = resizeEncodedForEdge(data, mime, edge)

	return data, mime, true
}

func resizeEncodedForEdge(data []byte, mime string, edge int) ([]byte, string) {
	edge = artwork.NormalizeThumbEdge(edge)
	if edge >= artwork.MaxEdge || len(data) == 0 {
		return data, mime
	}
	img, err := artwork.Decode(data)
	if err != nil {
		return data, mime
	}
	if b := img.Bounds(); b.Dx() <= edge && b.Dy() <= edge {
		return data, mime
	}
	out, outMime, err := artwork.Encode(artwork.Resize(img, edge))
	if err != nil {
		return data, mime
	}

	return out, outMime
}

func writeImageBytes(w http.ResponseWriter, r *http.Request, data []byte, mime, cacheControl string) {
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
