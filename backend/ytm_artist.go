package backend

import (
	"context"
	"fmt"
	"strings"

	ytm "github.com/dlcuy22/ytm-go"
)

// YTMArtistTrack is one playable YouTube Music song returned on an artist page
// or album/playlist listing.
type YTMArtistTrack struct {
	Ref        string `json:"ref"`
	NativeID   string `json:"nativeId"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	DurationMs int64  `json:"durationMs"`
	ImageURL   string `json:"imageUrl"`
}

// YTMArtistAlbum is one album or single card on a YouTube Music artist page.
type YTMArtistAlbum struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Artist   string `json:"artist"`
	ImageURL string `json:"imageUrl"`
	Year     int    `json:"year"`
}

// YTMArtistVideo is one video card on a YouTube Music artist page.
type YTMArtistVideo struct {
	ID           string `json:"id"`
	Ref          string `json:"ref"`
	Title        string `json:"title"`
	Subtitle     string `json:"subtitle"`
	DurationMs   int64  `json:"durationMs"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

// YTMArtistRef is one related artist entry on a YouTube Music artist page.
type YTMArtistRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ImageURL string `json:"imageUrl"`
	Subtitle string `json:"subtitle"`
}

// YTMArtistViewData is the frontend payload for YtmArtistView.
type YTMArtistViewData struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Description       string           `json:"description"`
	BannerURL         string           `json:"bannerUrl"`
	SubscriberText    string           `json:"subscriberText"`
	ShufflePlaylistID string           `json:"shufflePlaylistId"`
	TopSongs          []YTMArtistTrack `json:"topSongs"`
	Albums            []YTMArtistAlbum `json:"albums"`
	Singles           []YTMArtistAlbum `json:"singles"`
	Videos            []YTMArtistVideo `json:"videos"`
	RelatedArtists    []YTMArtistRef   `json:"relatedArtists"`
}

// YTMArtist loads a YouTube Music artist page by its browse/channel ID.
func (s *PlayerService) YTMArtist(artistID string) (YTMArtistViewData, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return YTMArtistViewData{}, fmt.Errorf("ytm: missing artist id")
	}
	if s.ytmProv == nil {
		return YTMArtistViewData{}, fmt.Errorf("ytm: provider unavailable")
	}
	loadArtist := s.ytmProv.calls().loadArtist
	if loadArtist == nil {
		return YTMArtistViewData{}, fmt.Errorf("ytm: artist loader unavailable")
	}

	ctx, cancel := context.WithTimeout(s.ctx, ytmHTTPTimeout)
	defer cancel()

	artist, err := loadArtist(ctx, artistID)
	if err != nil {
		return YTMArtistViewData{}, err
	}
	if artist == nil {
		return YTMArtistViewData{}, fmt.Errorf("ytm: artist %q not found", artistID)
	}

	return s.mapYTMArtist(artist), nil
}

// YTMArtistForTrack resolves the primary artist ID for a ytm:<videoId> track
// and loads that artist's page.
func (s *PlayerService) YTMArtistForTrack(ref, artistName string) (YTMArtistViewData, error) {
	if !isYTMRef(ref) {
		return YTMArtistViewData{}, fmt.Errorf("ytm: %q is not a YouTube Music track", ref)
	}
	if s.ytmProv == nil {
		return YTMArtistViewData{}, fmt.Errorf("ytm: provider unavailable")
	}

	ctx, cancel := context.WithTimeout(s.ctx, ytmHTTPTimeout)
	defer cancel()

	artistID := s.resolveYTMArtistID(ctx, ref, artistName)
	if artistID == "" {
		return YTMArtistViewData{}, fmt.Errorf("ytm: could not resolve artist for %q", ref)
	}

	return s.YTMArtist(artistID)
}

// YTMPlaylistTracks loads the tracks of a YouTube Music album or playlist so
// YtmArtistView album/single cards can open in PlaylistView or play directly.
func (s *PlayerService) YTMPlaylistTracks(playlistID string) ([]YTMArtistTrack, error) {
	playlistID = strings.TrimSpace(playlistID)
	if playlistID == "" {
		return nil, fmt.Errorf("ytm: missing playlist id")
	}
	if s.ytmProv == nil {
		return nil, fmt.Errorf("ytm: provider unavailable")
	}
	loadPlaylist := s.ytmProv.calls().loadPlaylist
	if loadPlaylist == nil {
		return nil, fmt.Errorf("ytm: playlist loader unavailable")
	}

	ctx, cancel := context.WithTimeout(s.ctx, ytmHTTPTimeout)
	defer cancel()

	pl, err := loadPlaylist(ctx, playlistID, nil)
	if err != nil {
		return nil, err
	}
	if pl == nil {
		return nil, nil
	}

	out := make([]YTMArtistTrack, 0, len(pl.Items))
	for i := range pl.Items {
		song := &pl.Items[i]
		if song.ID == "" {
			continue
		}
		tr := s.indexYTMSong(song, "", pl.Name)
		out = append(out, tr)
	}

	return out, nil
}

func (s *PlayerService) resolveYTMArtistID(ctx context.Context, ref, artistName string) string {
	if t, ok := s.ytm.lookup(ref); ok && t.ArtistID != "" {
		return t.ArtistID
	}
	if cat, ok := s.catalogYTMTrack(ref); ok && cat.ArtistID != "" {
		s.ytm.put(cat)

		return cat.ArtistID
	}

	calls := s.ytmProv.calls()
	videoID := ytmID(ref)
	if videoID != "" && calls.loadSong != nil {
		if song, err := calls.loadSong(ctx, videoID); err == nil && song != nil {
			for _, a := range song.Artists {
				if a.ID != "" {
					s.ytm.setArtist(ref, a.ID, "")

					return a.ID
				}
			}
		}
	}

	// Fallback: search by the first credited artist name when loadSong did not
	// carry a browse id.
	query := primaryArtistName(artistName)
	if query != "" && calls.search != nil {
		if res, err := calls.search(ctx, query, "ARTIST", false); err == nil && res != nil {
			for _, cat := range res.Categories {
				for _, item := range cat.Layout.Items {
					if a, ok := item.(*ytm.Artist); ok && a.ID != "" {
						s.ytm.setArtist(ref, a.ID, "")

						return a.ID
					}
					if a, ok := item.(ytm.Artist); ok && a.ID != "" {
						s.ytm.setArtist(ref, a.ID, "")

						return a.ID
					}
				}
			}
		}
	}

	return ""
}

func primaryArtistName(artist string) string {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return ""
	}
	for _, sep := range []string{",", "&", " feat.", " ft.", " x "} {
		if idx := strings.Index(strings.ToLower(artist), sep); idx > 0 {
			return strings.TrimSpace(artist[:idx])
		}
	}

	return artist
}

func (s *PlayerService) mapYTMArtist(artist *ytm.Artist) YTMArtistViewData {
	data := YTMArtistViewData{
		ID:                artist.ID,
		Name:              artist.Name,
		Description:       artist.Description,
		BannerURL:         ytmBannerURL(artist.Thumbnail),
		SubscriberText:    formatSubscribers(artist.SubscriberCount),
		ShufflePlaylistID: artist.ShufflePlaylistID,
		TopSongs:          []YTMArtistTrack{},
		Albums:            []YTMArtistAlbum{},
		Singles:           []YTMArtistAlbum{},
		Videos:            []YTMArtistVideo{},
		RelatedArtists:    []YTMArtistRef{},
	}

	for _, layout := range artist.Layouts {
		titleLower := strings.ToLower(layout.Title)
		for _, item := range layout.Items {
			switch v := item.(type) {
			case *ytm.Song:
				if v == nil || v.ID == "" {
					continue
				}
				if strings.Contains(titleLower, "video") || v.Type == ytm.SongTypeVideo {
					tr := s.indexYTMSong(v, artist.Name, "")
					data.Videos = append(data.Videos, YTMArtistVideo{
						ID:           v.ID,
						Ref:          tr.Ref,
						Title:        tr.Title,
						Subtitle:     tr.Artist,
						DurationMs:   tr.DurationMs,
						ThumbnailURL: tr.ImageURL,
					})
				} else {
					data.TopSongs = append(data.TopSongs, s.indexYTMSong(v, artist.Name, ""))
				}
			case ytm.Song:
				if v.ID == "" {
					continue
				}
				if strings.Contains(titleLower, "video") || v.Type == ytm.SongTypeVideo {
					tr := s.indexYTMSong(&v, artist.Name, "")
					data.Videos = append(data.Videos, YTMArtistVideo{
						ID:           v.ID,
						Ref:          tr.Ref,
						Title:        tr.Title,
						Subtitle:     tr.Artist,
						DurationMs:   tr.DurationMs,
						ThumbnailURL: tr.ImageURL,
					})
				} else {
					data.TopSongs = append(data.TopSongs, s.indexYTMSong(&v, artist.Name, ""))
				}
			case *ytm.Playlist:
				if v == nil || v.ID == "" {
					continue
				}
				alb := YTMArtistAlbum{
					ID:       v.ID,
					Name:     v.Name,
					Artist:   firstNonEmpty(joinYTMArtists(v.Artists), artist.Name),
					ImageURL: ytmThumb(v.Thumbnail),
					Year:     v.Year,
				}
				if strings.Contains(titleLower, "single") || strings.Contains(titleLower, "ep") {
					data.Singles = append(data.Singles, alb)
				} else if strings.Contains(titleLower, "album") || v.Type == ytm.PlaylistTypeAlbum {
					data.Albums = append(data.Albums, alb)
				}
			case ytm.Playlist:
				if v.ID == "" {
					continue
				}
				alb := YTMArtistAlbum{
					ID:       v.ID,
					Name:     v.Name,
					Artist:   firstNonEmpty(joinYTMArtists(v.Artists), artist.Name),
					ImageURL: ytmThumb(v.Thumbnail),
					Year:     v.Year,
				}
				if strings.Contains(titleLower, "single") || strings.Contains(titleLower, "ep") {
					data.Singles = append(data.Singles, alb)
				} else if strings.Contains(titleLower, "album") || v.Type == ytm.PlaylistTypeAlbum {
					data.Albums = append(data.Albums, alb)
				}
			case *ytm.Artist:
				if v == nil || v.ID == "" {
					continue
				}
				data.RelatedArtists = append(data.RelatedArtists, YTMArtistRef{
					ID:       v.ID,
					Name:     v.Name,
					ImageURL: ytmThumb(v.Thumbnail),
					Subtitle: formatSubscribers(v.SubscriberCount),
				})
			case ytm.Artist:
				if v.ID == "" {
					continue
				}
				data.RelatedArtists = append(data.RelatedArtists, YTMArtistRef{
					ID:       v.ID,
					Name:     v.Name,
					ImageURL: ytmThumb(v.Thumbnail),
					Subtitle: formatSubscribers(v.SubscriberCount),
				})
			}
		}
	}

	return data
}

func (s *PlayerService) indexYTMSong(song *ytm.Song, fallbackArtist, fallbackAlbum string) YTMArtistTrack {
	artist := firstNonEmpty(joinYTMArtists(song.Artists), fallbackArtist)
	album := fallbackAlbum
	if song.Album != nil && song.Album.Name != "" {
		album = song.Album.Name
	}
	thumb := ytmThumb(song.Thumbnail)
	artistID := ""
	for _, a := range song.Artists {
		if a.ID != "" {
			artistID = a.ID
			break
		}
	}
	s.ytm.put(ytmTrack{
		ID:           song.ID,
		Title:        song.Name,
		Artist:       artist,
		Album:        album,
		ArtistID:     artistID,
		DurationMs:   song.DurationMs,
		ThumbnailURL: thumb,
	})

	return YTMArtistTrack{
		Ref:        ytmRef(song.ID),
		NativeID:   song.ID,
		Title:      song.Name,
		Artist:     artist,
		Album:      album,
		DurationMs: song.DurationMs,
		ImageURL:   thumb,
	}
}

func joinYTMArtists(artists []ytm.Artist) string {
	names := make([]string, 0, len(artists))
	for _, a := range artists {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}

	return strings.Join(names, ", ")
}

// ytmBannerURL requests a wide 1440x600 banner when the ThumbnailProvider uses
// Google's parameterized w...-h... URL format, falling back to ytmThumb.
func ytmBannerURL(t *ytm.ThumbnailProvider) string {
	if t == nil {
		return ""
	}
	if t.UrlB != nil && !strings.HasPrefix(*t.UrlB, "https://") {
		return fmt.Sprintf("%s%d-h%d%s", t.UrlA, 1440, 600, *t.UrlB)
	}

	return ytmThumb(t)
}

func formatSubscribers(count int) string {
	if count <= 0 {
		return ""
	}
	switch {
	case count >= 1_000_000_000:
		return fmt.Sprintf("%.2gB subscribers", float64(count)/1_000_000_000)
	case count >= 1_000_000:
		return fmt.Sprintf("%.2fM subscribers", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%.1fK subscribers", float64(count)/1_000)
	default:
		return fmt.Sprintf("%d subscribers", count)
	}
}
