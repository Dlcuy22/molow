package lyrics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrackMeta is the search record one provider query is built from.
type TrackMeta struct {
	Title  string
	Artist string
	Album  string
	// DurationSec is the sound's length; 0 means unknown, and providers that
	// rely on it skip their duration-dependent steps.
	DurationSec int
}

// providerEndpoints holds the endpoints the chain calls. They live in a struct
// rather than loose constants so a test server can take over the whole chain;
// production reads the defaults below and never changes them.
type providerEndpoints struct {
	betterLyrics string   // full getLyrics URL
	lyricsPlus   []string // mirror bases
	binimum      string   // search base
	kugouSong    string
	kugouLyrics  string
	kugouFetch   string
	lrcGet       string
	lrcSearch    string
	petitlyrics  string // full GetPetitLyricsData.php URL
}

var providersEnd = providerEndpoints{
	betterLyrics: "https://lyrics-api.boidu.dev/getLyrics",
	lyricsPlus: []string{
		"https://lyricsplus.binimum.org",
		"https://lyricsplus.atomix.one",
		"https://lyricsplus.prjktla.my.id",
		"https://lyricsplus-seven.vercel.app",
	},
	binimum:     "https://lyrics-api.binimum.org?",
	kugouSong:   "https://mobileservice.kugou.com/api/v3/search/song?",
	kugouLyrics: "https://lyrics.kugou.com/search?",
	kugouFetch:  "https://lyrics.kugou.com/download?",
	lrcGet:      "https://lrclib.net/api/get?",
	lrcSearch:   "https://lrclib.net/api/search?",
	petitlyrics: "https://on.petitlyrics.com/api/GetPetitLyricsData.php",
}

// ChainProviders is the order the chain runs in, and a lower index wins a tie.
// PetitLyrics goes first, because its word-synced sheet ends the run and
// usually arrives in under a second.
var ChainProviders = []string{"petitlyrics", "betterlyrics", "lyricsplus", "kugou", "lrclib"}

// ChainBudget is the most one whole chain run may take, so a slow provider
// cannot outlast the patience a track change gets. providerTimeout is the
// ceiling for a single provider.
const ChainBudget = 10 * time.Second

// providerTimeout is the most one provider may take. It is shorter than the
// budget, so the chain can still reach the providers after it.
const providerTimeout = 6 * time.Second

// Fetch runs the chain and returns the winning LRC text and the provider it
// came from. A word-synced hit ends the run, a line-synced winner waits for
// something better, and one provider's failure never fails the chain. Debug
// lines go to log; nil means the default logger.
func Fetch(ctx context.Context, h *http.Client, meta TrackMeta, enabled map[string]bool, log *slog.Logger) (string, string, error) {
	if log == nil {
		log = slog.Default()
	}

	// The caller may bring its own deadline. Without one, the budget caps the
	// whole run rather than each provider.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ChainBudget)
		defer cancel()
	}

	best, bestSource, bestQ := "", "", 0
	for _, name := range ChainProviders {
		if enabled != nil && !enabled[name] {
			log.Debug("lyrics: provider disabled", "provider", name)

			continue
		}
		if ctx.Err() != nil {
			break
		}
		timeout := providerTimeout
		if d, ok := ctx.Deadline(); ok {
			if remaining := time.Until(d); remaining < timeout {
				timeout = remaining
			}
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		raw, err := fetchOne(pctx, h, name, meta)
		cancel()
		if err != nil || raw == "" {
			log.Debug("lyrics: provider missing", "provider", name, "error", err)

			continue
		}
		q := Quality(raw)
		log.Debug("lyrics: provider sheet", "provider", name, "quality", q, "bytes", len(raw))
		if q > bestQ {
			best, bestSource, bestQ = raw, name, q
		}
		if bestQ >= 3 {
			log.Debug("lyrics: word-synced ends the chain", "provider", bestSource, "quality", bestQ)

			return best, bestSource, nil
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("lyrics: no provider returned lyrics for %q", meta.Title)
	}

	log.Debug("lyrics: chain winner", "provider", bestSource, "quality", bestQ)

	return best, bestSource, nil
}

// fetchOne runs a named provider's query.
func fetchOne(cxt context.Context, h *http.Client, name string, meta TrackMeta) (string, error) {
	f := map[string]func(context.Context, *http.Client, TrackMeta) (string, error){
		"petitlyrics":  fetchPetitLyrics,
		"betterlyrics": fetchBetterLyrics,
		"lyricsplus":   fetchLyricsPlus,
		"kugou":        fetchKuGou,
		"lrclib":       fetchLrcLib,
	}[name]
	if f == nil {
		return "", fmt.Errorf("lyrics: unknown provider %q", name)
	}

	return f(cxt, h, meta)
}

// getJSON performs a provider's GET. A non-200 counts as that provider's miss,
// not as an error the chain reports. The body is capped, because no lyric
// endpoint returns more text than this.
func getJSON(ctx context.Context, h *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func getText(ctx context.Context, h *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := h.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}

	return string(raw), nil
}

// titleAliases returns the names to search the title under, best first. The
// full string leads, because the video-anchored searches need it to score over
// their threshold, and the bilingual split parts follow.
func titleAliases(meta TrackMeta) []string {
	aliases := unique(append([]string{meta.Title}, Aliases(meta.Title)...))

	out := aliases[:0]
	for _, a := range aliases {
		if strings.TrimSpace(a) != "" {
			out = append(out, a)
		}
	}

	return out
}

// artistAliases returns artist search candidates, the primary artist first.
func artistAliases(meta TrackMeta) []string {
	primary := PrimaryArtist(meta.Artist)
	aliases := unique(append([]string{primary}, Aliases(meta.Artist)...))

	out := aliases[:0]
	for _, a := range aliases {
		if a != "" {
			out = append(out, a)
		}
	}

	return out
}

// pairs returns the capped (artist, title) alias combinations one provider
// query might run over.
func pairs(meta TrackMeta, max int) [][2]string {
	var out [][2]string
	for _, t := range titleAliases(meta) {
		for _, a := range artistAliases(meta) {
			out = append(out, [2]string{a, t})
			if len(out) >= max {
				return out
			}
		}
	}

	return out
}

func unique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	return out
}

func fetchBetterLyrics(cxt context.Context, h *http.Client, meta TrackMeta) (string, error) {
	var body struct {
		TTML string `json:"ttml"`
	}
	for _, pair := range pairs(meta, 2) {
		q := url.Values{}
		q.Set("s", pair[1])
		q.Set("a", pair[0])
		if meta.Album != "" {
			q.Set("al", meta.Album)
		}
		if meta.DurationSec > 0 {
			q.Set("d", strconv.Itoa(meta.DurationSec))
		}
		if err := getJSON(cxt, h, providersEnd.betterLyrics+"?"+q.Encode(), &body); err != nil {
			continue
		}
		if doc := ParseTTML(body.TTML); doc != nil {
			return Serialize(doc), nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}

var (
	// lyricsPlusSticky is the mirror that answered last, tried first next time
	// so a working server is not rediscovered on every track.
	lyricsPlusMu     sync.Mutex
	lyricsPlusSticky string
)

type lyricsPlusResult struct {
	Type   string             `json:"type"`
	Lyrics []lyricsPlusSyntax `json:"lyrics"`
}

type lyricsPlusSyntax struct {
	Time     int64             `json:"time"`
	Text     string            `json:"text"`
	Syllabus []lyricsPlusSylla `json:"syllabus"`
}

type lyricsPlusSylla struct {
	Time     int64  `json:"time"`
	Duration int64  `json:"duration"`
	Text     string `json:"text"`
}

// fetchLyricsPlus tries the community mirrors and falls back to the binimum
// search when none has the track. The quality scorer judges whatever either
// stage returns.
func fetchLyricsPlus(ctx context.Context, h *http.Client, meta TrackMeta) (string, error) {
	for _, pair := range pairs(meta, 2) {
		if raw := fetchLyricsPlusMirrors(ctx, h, pair[0], pair[1], meta); raw != "" {
			return raw, nil
		}
	}

	return fetchBinimum(ctx, h, meta)
}

func fetchLyricsPlusMirrors(ctx context.Context, h *http.Client, artist, title string, meta TrackMeta) string {
	for _, base := range lyricsPlusStickyServers() {
		q := url.Values{}
		q.Set("title", title)
		q.Set("artist", artist)
		if meta.Album != "" {
			q.Set("album", meta.Album)
		}
		if meta.DurationSec > 0 {
			q.Set("duration", strconv.Itoa(meta.DurationSec))
		}
		var resp lyricsPlusResult
		if err := getJSON(ctx, h, base+"/v2/lyrics/get?"+q.Encode(), &resp); err != nil {
			continue
		}
		raw, err := lyricsPlusToLRC(&resp)
		if err != nil {
			continue
		}
		lyricsPlusMu.Lock()
		lyricsPlusSticky = base
		lyricsPlusMu.Unlock()

		return raw
	}

	return ""
}

// lyricsPlusStickyServers lists the mirrors, the last successful one first.
func lyricsPlusStickyServers() []string {
	lyricsPlusMu.Lock()
	sticky := lyricsPlusSticky
	lyricsPlusMu.Unlock()

	if sticky == "" {
		return providersEnd.lyricsPlus
	}

	out := []string{sticky}
	for _, s := range providersEnd.lyricsPlus {
		if s != sticky {
			out = append(out, s)
		}
	}

	return out
}

// lyricsPlusToLRC converts the JSON payload to canonical LRC. Word timing
// comes in per-line syllables, and a line without them is line-synced. Both
// shapes read back through Parse.
func lyricsPlusToLRC(resp *lyricsPlusResult) (string, error) {
	if resp == nil || len(resp.Lyrics) == 0 {
		return "", fmt.Errorf("empty")
	}

	var b strings.Builder
	for _, line := range resp.Lyrics {
		if line.Time < 0 {
			continue
		}
		b.WriteString(formatTime(line.Time))
		var words []Word
		for _, s := range line.Syllabus {
			if text := strings.TrimSpace(s.Text); text != "" {
				words = append(words, Word{Text: text, StartMs: s.Time, EndMs: s.Time + s.Duration})
			}
		}
		if len(words) > 0 {
			// The payload's own line text is canonical: it carries the
			// original spacing, which a rebuilt line can only guess at.
			if text := strings.TrimSpace(line.Text); text != "" {
				b.WriteString(text)
			} else {
				b.WriteString(joinWordTexts(words))
			}
			b.WriteByte('\n')
			b.WriteByte('<')
			for i, w := range words {
				if i > 0 {
					b.WriteByte('|')
				}
				fmt.Fprintf(&b, "%s:%.3f:%.3f", w.Text, float64(w.StartMs)/1000, float64(w.EndMs)/1000)
			}
			b.WriteString(">\n")
		} else {
			text := strings.TrimSpace(line.Text)
			if text == "" {
				continue
			}
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}

	if b.Len() == 0 {
		return "", fmt.Errorf("empty")
	}

	return strings.TrimSpace(b.String()), nil
}

// fetchBinimum is the search fallback: title and artist in, a TTML URL out,
// whose timing type only matters through the quality scorer.
func fetchBinimum(ctx context.Context, h *http.Client, meta TrackMeta) (string, error) {
	q := url.Values{}
	q.Set("track", titleAliases(meta)[0])
	if primary := PrimaryArtist(meta.Artist); primary != "" {
		q.Set("artist", primary)
	}
	if meta.Album != "" {
		q.Set("album", meta.Album)
	}
	if meta.DurationSec > 0 {
		q.Set("duration", strconv.Itoa(meta.DurationSec))
	}
	var resp struct {
		Results []struct {
			TimingType string `json:"timing_type"`
			LyricsURL  string `json:"lyricsUrl"`
		} `json:"results"`
	}
	if err := getJSON(ctx, h, providersEnd.binimum+q.Encode(), &resp); err != nil {
		return "", err
	}
	for _, r := range resp.Results {
		if r.LyricsURL == "" {
			continue
		}
		ttml, err := getText(ctx, h, r.LyricsURL)
		if err != nil {
			continue
		}
		if doc := ParseTTML(ttml); doc != nil {
			return Serialize(doc), nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}

// fetchKuGou searches by keyword: a song search by title and artist, a lyric
// search by the matching song's hash or the keyword, then a base64 download.
// The payload carries credit metadata, which the head and tail cut removes.
func fetchKuGou(ctx context.Context, h *http.Client, meta TrackMeta) (string, error) {
	q := url.Values{}
	q.Set("version", "9108")
	q.Set("plat", "0")
	q.Set("pagesize", "8")
	q.Set("showtype", "0")
	q.Set("keyword", kugouKeyword(meta))
	if q.Get("keyword") == "" {
		return "", fmt.Errorf("empty")
	}

	var songs struct {
		Data struct {
			Info []struct {
				Duration int    `json:"duration"`
				Hash     string `json:"hash"`
			} `json:"info"`
		} `json:"data"`
	}
	if err := getJSON(ctx, h, providersEnd.kugouSong+q.Encode(), &songs); err == nil {
		for _, song := range songs.Data.Info {
			if meta.DurationSec > 0 && absInt(song.Duration-meta.DurationSec) > kugouToleranceSec {
				continue
			}
			hq := url.Values{}
			hq.Set("ver", "1")
			hq.Set("man", "yes")
			hq.Set("client", "pc")
			hq.Set("hash", song.Hash)
			if id, key, ok := kugouCandidate(ctx, h, hq); ok {
				if raw := kugouDownload(ctx, h, id, key); raw != "" {
					return raw, nil
				}
			}
		}
	}

	// Without a matching song the lyric search runs on the keyword itself.
	kq := url.Values{}
	kq.Set("ver", "1")
	kq.Set("man", "yes")
	kq.Set("client", "pc")
	kq.Set("keyword", q.Get("keyword"))
	if meta.DurationSec > 0 {
		kq.Set("duration", strconv.Itoa(meta.DurationSec*1000))
	}
	if id, key, ok := kugouCandidate(ctx, h, kq); ok {
		if raw := kugouDownload(ctx, h, id, key); raw != "" {
			return raw, nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}

// kugouToleranceSec is how far a search result may differ in length from the
// track and still count as the same song.
const kugouToleranceSec = 8

// kugouCandidate runs one lyrics search and takes the first candidate.
func kugouCandidate(ctx context.Context, h *http.Client, q url.Values) (int64, string, bool) {
	var lyrics struct {
		Candidates []struct {
			Duration  int64  `json:"duration"`
			ID        int64  `json:"id"`
			AccessKey string `json:"accesskey"`
		} `json:"candidates"`
	}
	if err := getJSON(ctx, h, providersEnd.kugouLyrics+q.Encode(), &lyrics); err != nil {
		return 0, "", false
	}
	if len(lyrics.Candidates) == 0 {
		return 0, "", false
	}
	c := lyrics.Candidates[0]

	return c.ID, c.AccessKey, true
}

func kugouDownload(ctx context.Context, h *http.Client, id int64, accessKey string) string {
	q := url.Values{}
	q.Set("fmt", "lrc")
	q.Set("charset", "utf8")
	q.Set("client", "pc")
	q.Set("ver", "1")
	q.Set("id", strconv.FormatInt(id, 10))
	q.Set("accesskey", accessKey)
	var dl struct {
		Content string `json:"content"`
	}
	if err := getJSON(ctx, h, providersEnd.kugouFetch+q.Encode(), &dl); err != nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(dl.Content)
	if err != nil {
		return ""
	}

	return kugouNormalize(string(raw))
}

// kugouNormalize keeps timed lines and cuts the credit block KuGou puts at the
// head and tail, since a "Composer: ..." line would otherwise show up as a
// lyric. The cut follows the credit lines in from each end, however deep,
// because a scattered credit block means a broken payload.
func kugouNormalize(src string) string {
	var lines []string
	for _, line := range strings.Split(src, "\n") {
		if kugouAccepted.MatchString(line) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		return ""
	}

	head := 0
	for head < len(lines) && head < kugouCutLimit && kugouBanned.MatchString(lines[head]) {
		head++
	}
	tail := 0
	for tail < len(lines)-head && tail < kugouCutLimit && kugouBanned.MatchString(lines[len(lines)-1-tail]) {
		tail++
	}
	lines = lines[head : len(lines)-tail]
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n")
}

// kugouCutLimit bounds how deep into the file the credit cut may reach; the
// credit block is never the whole lyric sheet.
const kugouCutLimit = 30

var (
	// kugouAccepted matches one timestamped LRC line; anything else is
	// metadata or decoration.
	kugouAccepted = regexp.MustCompile(`^\[(\d\d):(\d\d)\.(\d{2,3})\]`)
	// kugouBanned matches a timestamped "label: value" credit line.
	kugouBanned = regexp.MustCompile(`^\[.*\].+[:：].+`)
)

// kugouKeyword builds the search string "title - artist" the endpoints expect,
// cleaned first so an "(Official MV)" title does not miss the catalogue.
func kugouKeyword(meta TrackMeta) string {
	title := CleanKey(meta.Title)
	if title == "" {
		title = meta.Title
	}
	artist := PrimaryArtist(meta.Artist)
	if artist == "" {
		artist = meta.Artist
	}

	return strings.TrimSpace(title + " - " + artist)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}

	return v
}

// fetchLrcLib runs the exact tuple get first, then a scored search: alias
// containment on both sides, duration within ±5 s, proximity as tie-breaker.
// LrcLib has no word tier, so its answer is line or plain at best.
func fetchLrcLib(ctx context.Context, h *http.Client, meta TrackMeta) (string, error) {
	var hit struct {
		SyncedLyrics string `json:"syncedLyrics"`
	}
	for _, pair := range pairs(meta, 4) {
		if meta.DurationSec <= 0 {
			continue
		}
		q := url.Values{}
		q.Set("artist_name", pair[0])
		q.Set("track_name", pair[1])
		if meta.Album != "" {
			q.Set("album_name", meta.Album)
		}
		q.Set("duration", strconv.Itoa(meta.DurationSec))
		if err := getJSON(ctx, h, providersEnd.lrcGet+q.Encode(), &hit); err == nil && hit.SyncedLyrics != "" {
			return hit.SyncedLyrics, nil
		}
	}

	var relaxed []lrclibTrack
	for _, pair := range pairs(meta, 4) {
		q := url.Values{}
		q.Set("artist_name", pair[0])
		q.Set("track_name", pair[1])
		var results []lrclibTrack
		if err := getJSON(ctx, h, providersEnd.lrcSearch+q.Encode(), &results); err != nil {
			continue
		}
		titles := append([]string{meta.Title}, titleAliases(meta)...)
		artists := append([]string{meta.Artist}, artistAliases(meta)...)
		for _, r := range results {
			if r.SyncedLyrics == "" {
				continue
			}
			if !contained(r.TrackName, titles) || !contained(r.ArtistName, artists) {
				continue
			}
			if meta.DurationSec > 0 && absInt(r.Duration-meta.DurationSec) > lrclibToleranceSec {
				continue
			}
			relaxed = append(relaxed, r)
		}
	}
	if len(relaxed) > 0 {
		if meta.DurationSec > 0 {
			sort.SliceStable(relaxed, func(i, j int) bool {
				return absInt(relaxed[i].Duration-meta.DurationSec) < absInt(relaxed[j].Duration-meta.DurationSec)
			})
		}
		if relaxed[0].SyncedLyrics != "" {
			return relaxed[0].SyncedLyrics, nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}

// lrclibToleranceSec keeps a search result within ±5 s of the sound's length;
// a closer match wins through the sort above.
const lrclibToleranceSec = 5

type lrclibTrack struct {
	TrackName    string `json:"trackName"`
	ArtistName   string `json:"artistName"`
	Duration     int    `json:"duration"`
	SyncedLyrics string `json:"syncedLyrics"`
	PlainLyrics  string `json:"plainLyrics"`
}

// contained matches with leniency in both directions, so a query in one
// language of a dual-language tag still finds the catalogue's other half.
func contained(target string, candidates []string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	for _, c := range candidates {
		low := strings.ToLower(strings.TrimSpace(c))
		if low == "" || t == "" {
			continue
		}
		if strings.Contains(t, low) || strings.Contains(low, t) {
			return true
		}
	}

	return false
}
