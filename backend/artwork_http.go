package backend

import (
	"context"
	"net/http"
	"strings"

	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	artRoutePrefix       = "/art/"
	artAvatarPrefix      = "avatar/"
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
		http.NotFound(w, r)

		return
	}

	if strings.HasPrefix(rest, artAvatarPrefix) {
		artistID := strings.TrimSpace(strings.TrimPrefix(rest, artAvatarPrefix))
		if artistID == "" || strings.ContainsAny(artistID, `/\`) {
			http.NotFound(w, r)

			return
		}
		data, mime, ok := s.ytmProv.artistAvatarBytes(r.Context(), artistID)
		if !ok {
			http.NotFound(w, r)

			return
		}
		writeImageBytes(w, r, data, mime, artVolatileCacheHdr)

		return
	}

	if strings.HasPrefix(rest, artYTMSearchPrefix) {
		videoID := strings.TrimSpace(strings.TrimPrefix(rest, artYTMSearchPrefix))
		if videoID == "" || strings.ContainsAny(videoID, `/\`) {
			http.NotFound(w, r)

			return
		}
		data, mime, ok := s.ytmSearchCoverBytes(r.Context(), ytmRef(videoID))
		if !ok {
			http.NotFound(w, r)

			return
		}
		writeImageBytes(w, r, data, mime, artVolatileCacheHdr)

		return
	}

	// Content-addressed artwork id from artwork.Store. Reject path separators
	// and dots before touching disk.
	id := rest
	if strings.ContainsAny(id, `/\.`) {
		http.NotFound(w, r)

		return
	}
	if s.art == nil {
		http.NotFound(w, r)

		return
	}

	if path, ok := s.art.FilePath(id); ok {
		w.Header().Set("Cache-Control", artImmutableCacheHdr)
		http.ServeFile(w, r, path)

		return
	}

	// Fallback 1: the active playing track carries embedded cover bytes that
	// have not been normalized onto disk yet (e.g. an unscanned dropped file).
	if s.player != nil {
		data := s.player.Snapshot().Meta.Tags.Cover
		if artwork.ID(data) == id {
			if _, err := s.art.Put(data); err == nil {
				if path, ok := s.art.FilePath(id); ok {
					w.Header().Set("Cache-Control", artImmutableCacheHdr)
					http.ServeFile(w, r, path)

					return
				}
			}
		}
	}

	// Fallback 2: a queued row passed its track path so its embedded or remote
	// cover can be materialized on first view.
	if trackPath := r.URL.Query().Get("path"); trackPath != "" {
		if path, ok := s.materializeQueueCover(r.Context(), id, trackPath); ok {
			w.Header().Set("Cache-Control", artImmutableCacheHdr)
			http.ServeFile(w, r, path)

			return
		}
	}

	http.NotFound(w, r)
}

func (s *PlayerService) materializeQueueCover(ctx context.Context, id, trackPath string) (string, bool) {
	if isYTMRef(trackPath) {
		t, ok := s.ytm.lookup(trackPath)
		if !ok {
			if cat, found := s.catalogYTMTrack(trackPath); found {
				s.ytm.put(cat)
				t, ok = cat, true
			}
		}
		if !ok {
			return "", false
		}
		if cid := t.coverID(); cid != "" {
			if p, ok := s.art.FilePath(cid); ok {
				return p, true
			}
		}
		data := s.ytmProv.fetchThumb(trackPath)
		if len(data) == 0 {
			return "", false
		}
		cid, err := s.art.Put(data)
		if err != nil {
			return "", false
		}

		return s.art.FilePath(cid)
	}

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

	return s.art.FilePath(id)
}

func (s *PlayerService) ytmSearchCoverBytes(ctx context.Context, ref string) ([]byte, string, bool) {
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
	// Reuse already-stored canonical art for tracks in the library catalogue
	// without triggering a network request or a new disk write.
	if s.art != nil && t.CoverID != "" {
		if data, mime, ok := s.art.Bytes(t.CoverID); ok {
			return data, mime, true
		}
	}

	return s.ytmProv.searchThumbBytes(ctx, ref, t.ThumbnailURL)
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
