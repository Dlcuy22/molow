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

// artistAvatarBytes resolves the artist's avatar and returns normalized image
// bytes and MIME type, or false when none is available.
func (p *ytmProvider) artistAvatarBytes(ctx context.Context, artistID string) ([]byte, string, bool) {
	return p.artistAvatarBytesForEdge(ctx, artistID, artwork.MaxEdge)
}

func (p *ytmProvider) artistAvatarBytesForEdge(ctx context.Context, artistID string, edge int) ([]byte, string, bool) {
	if artistID == "" {
		return nil, "", false
	}
	edge = artwork.NormalizeThumbEdge(edge)

	if p.art != nil {
		p.avatarMu.Lock()
		artID := p.avatarIDs[artistID]
		p.avatarMu.Unlock()
		if artID != "" {
			if data, mime, ok := p.art.ThumbBytes(artID, edge); ok {
				return data, mime, true
			}
		}
	}

	url := p.artistArt(ctx, artistID)
	if url == "" {
		return nil, "", false
	}
	data := p.thumbFor(ctx, url)
	if len(data) == 0 {
		return nil, "", false
	}

	if p.art != nil {
		artID, err := p.art.Put(data)
		if err != nil {
			return nil, "", false
		}
		p.avatarMu.Lock()
		p.avatarIDs[artistID] = artID
		p.avatarMu.Unlock()

		return p.art.ThumbBytes(artID, edge)
	}

	img, err := artwork.Decode(data)
	if err != nil {
		return nil, "", false
	}
	encoded, mime, err := artwork.Encode(artwork.Resize(img, edge))
	if err != nil {
		return nil, "", false
	}

	return encoded, mime, true
}

// artistAvatarDataURL resolves the artist's avatar and returns it as an inline
// data URL, or "" when there is none.
func (p *ytmProvider) artistAvatarDataURL(ctx context.Context, artistID string) string {
	data, mime, ok := p.artistAvatarBytes(ctx, artistID)
	if !ok {
		return ""
	}

	return artwork.FormatDataURL(data, mime)
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
	if p.art == nil {
		return ""
	}
	dir := p.art.Dir()
	if dir == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("artist:" + artistID))

	return filepath.Join(dir, "artist-"+strconv.FormatUint(h.Sum64(), 16)+".url")
}

// cachedArtistArt checks the in-memory index and the disk .url sidecar without
// touching the network. A returned true with an empty URL means the artist was
// already looked up and has no avatar.
func (p *ytmProvider) cachedArtistArt(artistID string) (string, bool) {
	if artistID == "" {
		return "", false
	}
	if p.artists != nil {
		if url, ok := p.artists.lookup(artistID); ok {
			return url, true
		}
	}
	urlPath := p.artistURLPath(artistID)
	if urlPath != "" {
		if data, err := os.ReadFile(urlPath); err == nil && len(data) > 0 {
			url := string(data)
			if p.artists != nil {
				p.artists.put(artistID, url)
			}

			return url, true
		}
	}

	return "", false
}

// artistArt resolves an artist's avatar URL. It reads the in-memory index, then
// the disk cache, then the browse endpoint, caching the outcome at every level.
// It never returns an error: an avatar is decorative, so a failure is "no
// avatar" rather than a reason to disturb the track.
func (p *ytmProvider) artistArt(ctx context.Context, artistID string) string {
	if artistID == "" {
		return ""
	}
	if url, ok := p.cachedArtistArt(artistID); ok {
		return url
	}
	loadArtist := p.calls().loadArtist
	if loadArtist == nil {
		return ""
	}

	artist, err := loadArtist(ctx, artistID)
	if err != nil || artist == nil {
		if p.artists != nil {
			p.artists.put(artistID, "")
		}

		return ""
	}
	url := ytmThumb(artist.Thumbnail)
	if p.artists != nil {
		p.artists.put(artistID, url)
	}
	urlPath := p.artistURLPath(artistID)
	if url != "" && urlPath != "" {
		if err := os.WriteFile(urlPath, []byte(url), 0o644); err != nil {
			p.logger().Warn("artist art: cache write failed", "artist", artistID, "error", err)
		}
	}

	return url
}
