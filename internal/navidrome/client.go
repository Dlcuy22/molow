// Package navidrome implements a minimal Subsonic / OpenSubsonic REST client
// for connecting MoLow to a Navidrome server.
package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiVersion = "1.16.1"
	clientName = "MoLow"

	defaultHTTPTimeout = 30 * time.Second
	maxCoverArtBytes   = 8 << 20
	maxJSONBytes       = 32 << 20
)

// Config holds the connection parameters for one Navidrome server.
type Config struct {
	ServerURL string `json:"serverUrl"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

// APIError is an application-level error returned inside a subsonic-response.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("navidrome: api error %d", e.Code)
	}

	return fmt.Sprintf("navidrome: %s (code %d)", e.Message, e.Code)
}

// PingInfo describes the server reported by ping.view.
type PingInfo struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Type          string `json:"type"`
	ServerVersion string `json:"serverVersion"`
	OpenSubsonic  bool   `json:"openSubsonic"`
}

// Artist represents an artist from Subsonic.
type Artist struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AlbumCount int    `json:"albumCount"`
	CoverArt   string `json:"coverArt"`
}

// Album represents an album from Subsonic.
type Album struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	ArtistID  string `json:"artistId"`
	CoverArt  string `json:"coverArt"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Year      int    `json:"year"`
	Genre     string `json:"genre"`
	Songs     []Song `json:"song"`
}

// Song represents a track from Subsonic.
type Song struct {
	ID          string `json:"id"`
	Parent      string `json:"parent"`
	Title       string `json:"title"`
	Album       string `json:"album"`
	AlbumID     string `json:"albumId"`
	Artist      string `json:"artist"`
	ArtistID    string `json:"artistId"`
	Track       int    `json:"track"`
	DiscNumber  int    `json:"discNumber"`
	Year        int    `json:"year"`
	Genre       string `json:"genre"`
	CoverArt    string `json:"coverArt"`
	Duration    int    `json:"duration"`
	Size        int64  `json:"size"`
	Suffix      string `json:"suffix"`
	ContentType string `json:"contentType"`
	BitRate     int    `json:"bitRate"`
	Starred     string `json:"starred"`
	Path        string `json:"path"`
}

// Playlist represents a playlist from Subsonic.
type Playlist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Comment   string `json:"comment"`
	Owner     string `json:"owner"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	CoverArt  string `json:"coverArt"`
	Entries   []Song `json:"entry"`
}

// StructuredLyrics models OpenSubsonic's getLyricsBySongId response item.
type StructuredLyrics struct {
	Lang   string      `json:"lang"`
	Synced bool        `json:"synced"`
	Lines  []LyricLine `json:"line"`
}

// LyricLine is one line in an OpenSubsonic structured lyrics response.
type LyricLine struct {
	Start int64  `json:"start"`
	Value string `json:"value"`
}

// Client speaks the Subsonic / OpenSubsonic REST API to a Navidrome server.
type Client struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
	stream  *http.Client
}

// NormalizeServerURL validates and canonicalizes a user-supplied server URL.
func NormalizeServerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("navidrome: server URL is required")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("navidrome: invalid server URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("navidrome: unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("navidrome: server URL has no host")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/rest")

	return strings.TrimRight(u.String(), "/"), nil
}

// NewClient constructs a Client from cfg. If httpClient is nil, a default client
// with a 30s timeout is used for metadata requests while stream requests omit a
// whole-body timeout.
func NewClient(cfg Config, httpClient *http.Client) (*Client, error) {
	base, err := NormalizeServerURL(cfg.ServerURL)
	if err != nil {
		return nil, err
	}
	user := strings.TrimSpace(cfg.Username)
	if user == "" {
		return nil, errors.New("navidrome: username is required")
	}
	if cfg.Password == "" {
		return nil, errors.New("navidrome: password is required")
	}

	redirectPolicy := sameOriginRedirectPolicy(base)
	metaClient := httpClient
	if metaClient == nil {
		metaClient = &http.Client{
			Timeout:       defaultHTTPTimeout,
			CheckRedirect: redirectPolicy,
		}
	}
	streamClient := httpClient
	if streamClient == nil {
		streamClient = &http.Client{
			CheckRedirect: redirectPolicy,
		}
	}

	return &Client{
		baseURL: base,
		user:    user,
		pass:    cfg.Password,
		http:    metaClient,
		stream:  streamClient,
	}, nil
}

// BaseURL returns the normalized server base URL.
func (c *Client) BaseURL() string { return c.baseURL }

func sameOriginRedirectPolicy(baseURL string) func(req *http.Request, via []*http.Request) error {
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("navidrome: stopped after 10 redirects")
		}
		if !strings.EqualFold(req.URL.Scheme, parsedBase.Scheme) || !strings.EqualFold(req.URL.Host, parsedBase.Host) {
			return fmt.Errorf("navidrome: blocked cross-origin redirect to %s://%s", req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
}

func randomSalt() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(b[:]), nil
}

func authToken(password, salt string) string {
	sum := md5.Sum([]byte(password + salt))

	return hex.EncodeToString(sum[:])
}

func (c *Client) endpoint(method string, params url.Values) (string, error) {
	salt, err := randomSalt()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	q.Set("u", c.user)
	q.Set("t", authToken(c.pass, salt))
	q.Set("s", salt)
	q.Set("v", apiVersion)
	q.Set("c", clientName)
	q.Set("f", "json")

	return c.baseURL + "/rest/" + method + ".view?" + q.Encode(), nil
}

func sanitizeURLError(err error) error {
	if err == nil {
		return nil
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if parsed, perr := url.Parse(uerr.URL); perr == nil {
			q := parsed.Query()
			redacted := false
			for _, param := range []string{"t", "s", "p", "u"} {
				if q.Has(param) {
					q.Set(param, "REDACTED")
					redacted = true
				}
			}
			if redacted {
				parsed.RawQuery = q.Encode()
				return &url.Error{
					Op:  uerr.Op,
					URL: parsed.String(),
					Err: uerr.Err,
				}
			}
		}
	}
	return err
}

type subsonicEnvelope struct {
	Root struct {
		Status        string    `json:"status"`
		Version       string    `json:"version"`
		Type          string    `json:"type"`
		ServerVersion string    `json:"serverVersion"`
		OpenSubsonic  bool      `json:"openSubsonic"`
		Error         *APIError `json:"error"`

		AlbumList2 *struct {
			Album []Album `json:"album"`
		} `json:"albumList2"`
		Album     *Album `json:"album"`
		Song      *Song  `json:"song"`
		Playlists *struct {
			Playlist []Playlist `json:"playlist"`
		} `json:"playlists"`
		Playlist      *Playlist `json:"playlist"`
		SearchResult3 *struct {
			Artist []Artist `json:"artist"`
			Album  []Album  `json:"album"`
			Song   []Song   `json:"song"`
		} `json:"searchResult3"`
		Starred2 *struct {
			Song []Song `json:"song"`
		} `json:"starred2"`
		LyricsList *struct {
			StructuredLyrics []StructuredLyrics `json:"structuredLyrics"`
		} `json:"lyricsList"`
		Lyrics *struct {
			Artist string `json:"artist"`
			Title  string `json:"title"`
			Value  string `json:"value"`
		} `json:"lyrics"`
	} `json:"subsonic-response"`
}

func (c *Client) getJSON(ctx context.Context, method string, params url.Values) (*subsonicEnvelope, error) {
	u, err := c.endpoint(method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, sanitizeURLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("navidrome: %s returned HTTP %d", method, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return nil, err
	}
	var env subsonicEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("navidrome: decode %s response: %w", method, err)
	}
	if env.Root.Status != "ok" {
		if env.Root.Error != nil {
			return nil, env.Root.Error
		}

		return nil, fmt.Errorf("navidrome: %s failed", method)
	}

	return &env, nil
}

// Ping verifies server reachability and credentials.
func (c *Client) Ping(ctx context.Context) (PingInfo, error) {
	env, err := c.getJSON(ctx, "ping", nil)
	if err != nil {
		return PingInfo{}, err
	}

	return PingInfo{
		Status:        env.Root.Status,
		Version:       env.Root.Version,
		Type:          env.Root.Type,
		ServerVersion: env.Root.ServerVersion,
		OpenSubsonic:  env.Root.OpenSubsonic,
	}, nil
}

// AlbumList fetches a page of albums sorted alphabetically by name.
func (c *Client) AlbumList(ctx context.Context, size, offset int) ([]Album, error) {
	if size <= 0 {
		size = 500
	}
	q := url.Values{
		"type":   {"alphabeticalByName"},
		"size":   {strconv.Itoa(size)},
		"offset": {strconv.Itoa(offset)},
	}
	env, err := c.getJSON(ctx, "getAlbumList2", q)
	if err != nil {
		return nil, err
	}
	if env.Root.AlbumList2 == nil {
		return nil, nil
	}

	return env.Root.AlbumList2.Album, nil
}

// GetAlbum fetches one album and its ordered tracks.
func (c *Client) GetAlbum(ctx context.Context, id string) (Album, error) {
	env, err := c.getJSON(ctx, "getAlbum", url.Values{"id": {id}})
	if err != nil {
		return Album{}, err
	}
	if env.Root.Album == nil {
		return Album{}, fmt.Errorf("navidrome: album %q not found", id)
	}

	return *env.Root.Album, nil
}

// GetSong fetches metadata for one song ID.
func (c *Client) GetSong(ctx context.Context, id string) (Song, error) {
	env, err := c.getJSON(ctx, "getSong", url.Values{"id": {id}})
	if err != nil {
		return Song{}, err
	}
	if env.Root.Song == nil {
		return Song{}, fmt.Errorf("navidrome: song %q not found", id)
	}

	return *env.Root.Song, nil
}

// GetPlaylists fetches every playlist visible to the authenticated user.
func (c *Client) GetPlaylists(ctx context.Context) ([]Playlist, error) {
	env, err := c.getJSON(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, err
	}
	if env.Root.Playlists == nil {
		return nil, nil
	}

	return env.Root.Playlists.Playlist, nil
}

// GetPlaylist fetches one playlist and its ordered entries.
func (c *Client) GetPlaylist(ctx context.Context, id string) (Playlist, error) {
	env, err := c.getJSON(ctx, "getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return Playlist{}, err
	}
	if env.Root.Playlist == nil {
		return Playlist{}, fmt.Errorf("navidrome: playlist %q not found", id)
	}

	return *env.Root.Playlist, nil
}

// CreatePlaylist creates a playlist on the server and returns the created row.
func (c *Client) CreatePlaylist(ctx context.Context, name string, songIDs []string) (Playlist, error) {
	q := url.Values{"name": {name}}
	for _, id := range songIDs {
		if id != "" {
			q.Add("songId", id)
		}
	}
	env, err := c.getJSON(ctx, "createPlaylist", q)
	if err != nil {
		return Playlist{}, err
	}
	if env.Root.Playlist != nil {
		return *env.Root.Playlist, nil
	}
	lists, err := c.GetPlaylists(ctx)
	if err == nil {
		for _, pl := range lists {
			if pl.Name == name {
				return pl, nil
			}
		}
	}

	return Playlist{Name: name}, nil
}

// UpdatePlaylist mutates a playlist's name, added songs, or removed positions.
func (c *Client) UpdatePlaylist(ctx context.Context, playlistID string, name *string, addIDs []string, removeIndexes []int) error {
	q := url.Values{"playlistId": {playlistID}}
	if name != nil {
		q.Set("name", *name)
	}
	for _, id := range addIDs {
		if id != "" {
			q.Add("songIdToAdd", id)
		}
	}
	for _, idx := range removeIndexes {
		q.Add("songIndexToRemove", strconv.Itoa(idx))
	}
	_, err := c.getJSON(ctx, "updatePlaylist", q)

	return err
}

// DeletePlaylist removes a playlist from the server.
func (c *Client) DeletePlaylist(ctx context.Context, playlistID string) error {
	_, err := c.getJSON(ctx, "deletePlaylist", url.Values{"id": {playlistID}})

	return err
}

// StarredSongs fetches the user's starred tracks via getStarred2.
func (c *Client) StarredSongs(ctx context.Context) ([]Song, error) {
	env, err := c.getJSON(ctx, "getStarred2", nil)
	if err != nil {
		return nil, err
	}
	if env.Root.Starred2 == nil {
		return nil, nil
	}

	return env.Root.Starred2.Song, nil
}

// SetStarred stars or unstars a track on the server.
func (c *Client) SetStarred(ctx context.Context, songID string, starred bool) error {
	method := "unstar"
	if starred {
		method = "star"
	}
	_, err := c.getJSON(ctx, method, url.Values{"id": {songID}})

	return err
}

// Search queries search3.view for matching songs, albums, and artists.
func (c *Client) Search(ctx context.Context, query string, songCount, albumCount, artistCount int) ([]Song, []Album, []Artist, error) {
	q := url.Values{
		"query":       {query},
		"songCount":   {strconv.Itoa(songCount)},
		"albumCount":  {strconv.Itoa(albumCount)},
		"artistCount": {strconv.Itoa(artistCount)},
	}
	env, err := c.getJSON(ctx, "search3", q)
	if err != nil {
		return nil, nil, nil, err
	}
	if env.Root.SearchResult3 == nil {
		return nil, nil, nil, nil
	}

	return env.Root.SearchResult3.Song, env.Root.SearchResult3.Album, env.Root.SearchResult3.Artist, nil
}

// SearchSongs queries search3.view paginated by songOffset.
func (c *Client) SearchSongs(ctx context.Context, query string, songCount, songOffset int) ([]Song, error) {
	q := url.Values{
		"query":       {query},
		"songCount":   {strconv.Itoa(songCount)},
		"songOffset":  {strconv.Itoa(songOffset)},
		"albumCount":  {"0"},
		"artistCount": {"0"},
	}
	env, err := c.getJSON(ctx, "search3", q)
	if err != nil {
		return nil, err
	}
	if env.Root.SearchResult3 == nil {
		return nil, nil
	}

	return env.Root.SearchResult3.Song, nil
}

// GetCoverArt downloads artwork bytes for coverID.
func (c *Client) GetCoverArt(ctx context.Context, coverID string, size int) ([]byte, string, error) {
	if coverID == "" {
		return nil, "", errors.New("navidrome: empty cover id")
	}
	q := url.Values{"id": {coverID}}
	if size > 0 {
		q.Set("size", strconv.Itoa(size))
	}
	u, err := c.endpoint("getCoverArt", q)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", sanitizeURLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("navidrome: getCoverArt returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverArtBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxCoverArtBytes {
		return nil, "", errors.New("navidrome: cover art exceeds size limit")
	}
	if len(data) == 0 {
		return nil, "", errors.New("navidrome: empty cover art")
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "json") || strings.Contains(ct, "xml") {
		return nil, "", fmt.Errorf("navidrome: cover art returned %s", ct)
	}

	return data, ct, nil
}

// OpenStream opens an HTTP stream response for songID (raw original audio).
func (c *Client) OpenStream(ctx context.Context, songID string, headers http.Header) (*http.Response, error) {
	if songID == "" {
		return nil, errors.New("navidrome: empty song id")
	}
	u, err := c.endpoint("stream", url.Values{"id": {songID}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, sanitizeURLError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("navidrome: stream returned HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "json") || strings.Contains(ct, "xml") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
		_ = resp.Body.Close()
		if strings.Contains(ct, "json") {
			var env subsonicEnvelope
			if err := json.Unmarshal(body, &env); err == nil && env.Root.Error != nil {
				return nil, env.Root.Error
			}
		}
		return nil, fmt.Errorf("navidrome: stream returned %s", ct)
	}

	return resp, nil
}

// GetLyrics fetches lyrics from the server (OpenSubsonic getLyricsBySongId first,
// falling back to getLyrics), returning LRC or plain text and whether it is synced.
func (c *Client) GetLyrics(ctx context.Context, songID, artist, title string) (string, bool, error) {
	if songID != "" {
		if env, err := c.getJSON(ctx, "getLyricsBySongId", url.Values{"id": {songID}}); err == nil && env.Root.LyricsList != nil {
			var plainFallback string
			for _, item := range env.Root.LyricsList.StructuredLyrics {
				if len(item.Lines) == 0 {
					continue
				}
				if item.Synced {
					return formatStructuredLRC(item.Lines), true, nil
				}
				if plainFallback == "" {
					plainFallback = formatPlainLyrics(item.Lines)
				}
			}
			if plainFallback != "" {
				return plainFallback, false, nil
			}
		}
	}

	if artist != "" || title != "" {
		q := url.Values{}
		if artist != "" {
			q.Set("artist", artist)
		}
		if title != "" {
			q.Set("title", title)
		}
		if env, err := c.getJSON(ctx, "getLyrics", q); err == nil && env.Root.Lyrics != nil {
			text := strings.TrimSpace(env.Root.Lyrics.Value)
			if text != "" {
				return text, false, nil
			}
		}
	}

	return "", false, nil
}

// Scrobble notifies Navidrome of playback start (submission=false) or completion (submission=true).
func (c *Client) Scrobble(ctx context.Context, songID string, submission bool) error {
	if songID == "" {
		return nil
	}
	q := url.Values{
		"id":         {songID},
		"submission": {strconv.FormatBool(submission)},
		"time":       {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	_, err := c.getJSON(ctx, "scrobble", q)

	return err
}

func formatStructuredLRC(lines []LyricLine) string {
	var b strings.Builder
	for _, line := range lines {
		ms := line.Start
		if ms < 0 {
			ms = 0
		}
		totalSec := ms / 1000
		min := totalSec / 60
		sec := totalSec % 60
		cs := (ms % 1000) / 10
		fmt.Fprintf(&b, "[%02d:%02d.%02d]%s\n", min, sec, cs, line.Value)
	}

	return b.String()
}

func formatPlainLyrics(lines []LyricLine) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line.Value)
	}

	return b.String()
}
