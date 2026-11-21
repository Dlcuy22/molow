// Artist avatars for the Discord presence's small image.
//
// YouTube Music's search and watch responses name the credited artists but do
// not carry their images: the avatar lives behind the artist's browse page. So
// this is a second, deliberate lookup, done once per artist and cached twice:
// in memory for the session, and on disk so a later run does not spend the
// request again. A miss is a normal outcome and reads as "no avatar".
//
// The cached value is the resolved https URL, not the image bytes. Discord
// fetches an https URL itself, so the URL is the only thing the presence needs;
// the disk entry is what stops the browse call from being repeated.

package backend

import (
	"context"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/dlcuy22/molow/internal/artwork"
)

// ytmArtistIndex caches the resolved avatar URL per artist browse id. A present
// entry with an empty URL means the artist was looked up and has none, which is
// cached too so a coverless artist is not re-queried on every track.
type ytmArtistIndex struct {
	mu   sync.Mutex
	urls map[string]string
}

func newYTMArtistIndex() *ytmArtistIndex {
	return &ytmArtistIndex{urls: make(map[string]string)}
}

func (a *ytmArtistIndex) lookup(id string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	url, ok := a.urls[id]

	return url, ok
}

func (a *ytmArtistIndex) put(id, url string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.urls[id] = url
}

// artistAvatarDataURL resolves the artist's avatar and returns it as an inline
// data URL, or "" when there is none. Fetched bytes are normalized once into
// the shared artwork store so data URLs share its bounded LRU.
func (p *ytmProvider) artistAvatarDataURL(ctx context.Context, artistID string) string {
	if artistID == "" {
		return ""
	}

	if p.art != nil {
		p.avatarMu.Lock()
		artID := p.avatarIDs[artistID]
		p.avatarMu.Unlock()
		if artID != "" {
			if url, ok := p.art.DataURL(artID); ok {
				return url
			}
		}
	}

	url := p.artistArt(ctx, artistID)
	if url == "" {
		return ""
	}
	data := p.thumbFor(ctx, url)
	if len(data) == 0 {
		return ""
	}

	if p.art != nil {
		artID, err := p.art.Put(data)
		if err != nil {
			return ""
		}
		p.avatarMu.Lock()
		p.avatarIDs[artistID] = artID
		p.avatarMu.Unlock()
		dataURL, _ := p.art.DataURL(artID)

		return dataURL
	}

	img, err := artwork.Decode(data)
	if err != nil {
		return ""
	}
	encoded, mime, err := artwork.Encode(artwork.Resize(img, artwork.MaxEdge))
	if err != nil {
		return ""
	}

	return artwork.FormatDataURL(encoded, mime)
}

// logger returns the provider's logger, falling back to the default so a
// provider built directly in a test cannot nil-panic on a log line.
func (p *ytmProvider) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}

	return slog.Default()
}

// artistURLPath returns a dedicated .url sidecar inside the artwork cache dir
// so plain URL text never mixes with canonical .jpg/.png image files.
func (p *ytmProvider) artistURLPath(artistID string) string {
	dir := p.art.Dir()
	if dir == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("artist:" + artistID))

	return filepath.Join(dir, "artist-"+strconv.FormatUint(h.Sum64(), 16)+".url")
}

// artistArt resolves an artist's avatar URL. It reads the in-memory index, then
// the disk cache, then the browse endpoint, caching the outcome at every level.
// It never returns an error: an avatar is decorative, so a failure is "no
// avatar" rather than a reason to disturb the track.
func (p *ytmProvider) artistArt(ctx context.Context, artistID string) string {
	loadArtist := p.calls().loadArtist
	if artistID == "" || loadArtist == nil {
		return ""
	}
	if url, ok := p.artists.lookup(artistID); ok {
		return url
	}

	urlPath := p.artistURLPath(artistID)
	if urlPath != "" {
		if data, err := os.ReadFile(urlPath); err == nil && len(data) > 0 {
			url := string(data)
			p.artists.put(artistID, url)

			return url
		}
	}

	artist, err := loadArtist(ctx, artistID)
	if err != nil || artist == nil {
		p.artists.put(artistID, "")

		return ""
	}
	url := ytmThumb(artist.Thumbnail)
	p.artists.put(artistID, url)
	if url != "" && urlPath != "" {
		if err := os.WriteFile(urlPath, []byte(url), 0o644); err != nil {
			p.logger().Warn("artist art: cache write failed", "artist", artistID, "error", err)
		}
	}

	return url
}
