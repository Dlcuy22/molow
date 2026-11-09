// Package library is the browsing half of the app: the libraries a user has,
// the catalogue inside them, and the optional capabilities each one advertises.
//
// It imports neither the UI nor the engine. A library describes what can be
// browsed and played; the engine still resolves the opaque refs it emits
// (absolute paths and ytm:<videoId> today).
package library

import "context"

// Kind names the provider behind a library. One provider can back many
// libraries: two folders are two local libraries.
type Kind string

const (
	KindLocal Kind = "local"
	KindYTM   Kind = "ytm"
)

// Capability is one optional thing a library can do. The set travels as data,
// so the frontend never type-asserts and never renders an action that cannot
// work.
type Capability string

const (
	CapFeed         Capability = "feed"
	CapSearch       Capability = "search"
	CapLike         Capability = "like"
	CapPlaylist     Capability = "playlist"
	CapPlaylistEdit Capability = "playlist.edit"
	CapSignIn       Capability = "signIn"
)

// Status is the one-line health of a library. It is the only place a library's
// kind becomes visible in the UI.
type Status string

const (
	StatusReady     Status = "ready"
	StatusScanning  Status = "scanning"
	StatusSignedOut Status = "signedOut"
	StatusExpired   Status = "expired"
	StatusError     Status = "error"
)

// Flags travel with a row so a list renders without a second call per item.
type Flags uint32

const (
	FlagPlayable Flags = 1 << iota
	FlagLikeable
	FlagPlaylistAddable
	FlagSaved
)

// Library is one browsable instance.
type Library struct {
	ID           string       `json:"id"`
	Kind         Kind         `json:"kind"`
	Name         string       `json:"name"`
	Root         string       `json:"root,omitempty"`
	Status       Status       `json:"status"`
	Message      string       `json:"message,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	TrackCount   int          `json:"trackCount"`
}

// Track is one playable entry. Ref is what the engine plays.
type Track struct {
	Ref         string `json:"ref"`
	LibraryID   string `json:"libraryId"`
	NativeID    string `json:"nativeId"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Year        int    `json:"year,omitempty"`
	TrackNo     int    `json:"trackNo,omitempty"`
	DiscNo      int    `json:"discNo,omitempty"`
	DurationMs  int64  `json:"durationMs"`
	CoverID     string `json:"coverId,omitempty"`
	Liked       bool   `json:"liked"`
	Flags       Flags  `json:"flags"`
}

// Album is a grouping of catalogue tracks. It is derived, never stored: the
// rows are the truth and an album is a query.
type Album struct {
	ID         string `json:"id"`
	LibraryID  string `json:"libraryId"`
	Name       string `json:"name"`
	Artist     string `json:"artist"`
	CoverID    string `json:"coverId,omitempty"`
	SongCount  int    `json:"songCount"`
	DurationMs int64  `json:"durationMs"`
	Year       int    `json:"year,omitempty"`
}

// Artist is a grouping of catalogue tracks by artist name.
type Artist struct {
	ID         string `json:"id"`
	LibraryID  string `json:"libraryId"`
	Name       string `json:"name"`
	CoverID    string `json:"coverId,omitempty"`
	SongCount  int    `json:"songCount"`
	AlbumCount int    `json:"albumCount"`
}

// Playlist is a user-ordered list. Local playlists live in SQLite; a remote
// one carries its remote id.
type Playlist struct {
	ID          string `json:"id"`
	LibraryID   string `json:"libraryId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TrackCount  int    `json:"trackCount"`
}

// Shelf is one horizontal row of a page. Layout says how to draw it; kind says
// what the items are. Albums is additive to the plan's track-only shelf because
// the design's home and library rows are album cards, not tracks.
type Shelf struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Subtitle  string     `json:"subtitle,omitempty"`
	Layout    string     `json:"layout"` // "list" | "grid"
	Kind      string     `json:"kind"`   // "tracks" | "albums" | "artists" | "playlists"
	Items     []Track    `json:"items,omitempty"`
	Albums    []Album    `json:"albums,omitempty"`
	Artists   []Artist   `json:"artists,omitempty"`
	Playlists []Playlist `json:"playlists,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
	Total     int        `json:"total,omitempty"`
}

// Page is one response from a feed.
type Page struct {
	Shelves []Shelf `json:"shelves"`
	Cursor  string  `json:"cursor,omitempty"`
}

// Source is the browsing contract every library implements.
type Source interface {
	Describe(ctx context.Context) (Library, error)
	Feed(ctx context.Context, tab, cursor string, limit int) (Page, error)
	Tracks(ctx context.Context, refs []string) ([]Track, error)
}

// Searchable is optional full-text search.
type Searchable interface {
	Search(ctx context.Context, query string, limit int) (Page, error)
}

// Likable is optional favourite state.
type Likable interface {
	SetLiked(ctx context.Context, ref string, liked bool) error
}

// PlaylistReader reads user playlists.
type PlaylistReader interface {
	Playlists(ctx context.Context) ([]Playlist, error)
	PlaylistTracks(ctx context.Context, playlistID, cursor string, limit int) (Page, error)
}

// PlaylistWriter changes playlists.
type PlaylistWriter interface {
	PlaylistReader
	CreatePlaylist(ctx context.Context, name string) (Playlist, error)
	AddToPlaylist(ctx context.Context, playlistID string, refs []string) error
	RemoveFromPlaylist(ctx context.Context, playlistID string, refs []string) error
}

// Authenticator is optional account sign-in.
type Authenticator interface {
	SignIn(ctx context.Context) error
	SignOut(ctx context.Context) error
	Auth(ctx context.Context) (AuthStatus, error)
}

// AuthStatus is a remote library's session state, mirroring the YTM client's.
type AuthStatus struct {
	SignedIn bool   `json:"signedIn"`
	Channel  string `json:"channel,omitempty"`
}

// CapabilitiesOf reports what a source can do. It is computed once when the
// library is registered.
func CapabilitiesOf(s Source) []Capability {
	caps := []Capability{CapFeed}
	if _, ok := s.(Searchable); ok {
		caps = append(caps, CapSearch)
	}
	if _, ok := s.(Likable); ok {
		caps = append(caps, CapLike)
	}
	if _, ok := s.(PlaylistReader); ok {
		caps = append(caps, CapPlaylist)
	}
	if _, ok := s.(PlaylistWriter); ok {
		caps = append(caps, CapPlaylistEdit)
	}
	if _, ok := s.(Authenticator); ok {
		caps = append(caps, CapSignIn)
	}

	return caps
}
