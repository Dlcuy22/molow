// Metadata cleaning for lyric search and sidecar matching: the channel and
// marketing decorations YouTube Music tags carry, which no lyric catalogue has
// ever stored. Every provider query and every sidecar match runs through here.

package lyrics

import (
	"regexp"
	"strings"
)

var (
	// junkWords are the fixed channel decorations, stripped wherever found.
	junkWords = []string{" - Topic", " official YouTube channel", "VEVO"}

	// officialCase cuts a trailing "Official"/"Official Video" marker, the
	// channel-title form outside brackets. The cut needs the whitespace before
	// it, or it would slice "Rammstein" into "Rammstei".
	officialCase = regexp.MustCompile(`(?i)\s+official(?:\s+\S+){0,2}\s*$`)

	// dualLang splits the dual-language forms: a spaced slash, a spaced dash
	// (the hyphen, en and em dash forms YouTube Music titles carry), and a run
	// of spaces. A bare slash stays, since "AC/DC" is a band, not two names.
	dualLang = regexp.MustCompile(`(?:\s+/|\s+[\p{Pd}]\s+|\s{2,})\s*`)

	// bracketJunk removes a bracket group whose content is marketing: official
	// video markers, "Lyrics", "MV", "HD". Real content such as "(Live at
	// Budokan)" stays, because it tells releases apart.
	bracketJunk = regexp.MustCompile(`(?i)\s*[\[\(\{【〔〈《][^)\]\}】〕〉》]*\b(official|video|audio|lyrics?|mv|visualizer|remaster(?:ed)?|hd|hq|4k)\b[^)\]\}】〕〉》]*[)\]\}】〕〉》]`)

	// yearBracket removes a bracket holding nothing but a year, and only the
	// bracketed form: a title that ends in a number is not an edition.
	yearBracket = regexp.MustCompile(`\s*[(\[【〔〈《]\s*(?:19|20)\d{2}\s*[)\]】〕〉》]\s*$`)

	// featTail cuts a featured credit off the end: "(feat. X)" and
	// "feat. X" alike.
	featTail = regexp.MustCompile(`(?i)\s*[(\[]?\s*\b(?:feat\.?|ft\.?|featuring)\b.*$`)

	// pipeTail cuts a trailing " | channel name" decoration.
	pipeTail = regexp.MustCompile(`\s*[|｜].*$`)
)

// Aliases returns the search names for one metadata string. Dual-language
// tags such as "なとり / natori" split, because the lyric catalogues may hold
// either half. A plain string comes back as a single cleaned alias.
func Aliases(s string) []string {
	s = scrub(s)
	if s == "" {
		return nil
	}

	if parts := dualLang.Split(s, -1); len(parts) > 1 {
		var aliases []string
		for _, p := range parts {
			if cleaned := strings.TrimSpace(p); cleaned != "" {
				aliases = append(aliases, cleaned)
			}
		}
		if len(aliases) > 0 {
			return aliases
		}
	}

	return []string{s}
}

// CleanKey is the cleaned form a sidecar name is matched against: the channel
// junk and the marketing brackets removed, one alias. The fuzzy tier of find's
// matching runs both sides through this, so a hand-written
// "Song (Official MV).lrc" still lands beside "Song.flac".
func CleanKey(s string) string {
	aliases := Aliases(s)
	title := ""
	if len(aliases) > 0 {
		title = aliases[0]
	}
	title = stripBrackets(title)
	title = pipeTail.ReplaceAllString(title, "")

	return strings.TrimSpace(title)
}

// Filename is CleanKey made safe as a file name: the characters a desktop
// path cannot carry become a space.
func Filename(title string) string {
	repl := strings.NewReplacer(
		"/", " ", `\`, " ", ":", " ", "*", " ", "?", " ",
		`"`, " ", "<", " ", ">", " ", "|", " ",
	)

	return strings.TrimSpace(repl.Replace(CleanKey(title)))
}

// PrimaryArtist is the artist a provider should search first: the credits
// before the first feature or collaboration, dual-language aliases still
// intact. A song credited "A & B" is catalogued under A.
func PrimaryArtist(artist string) string {
	a := scrub(artist)
	lower := strings.ToLower(a)
	for _, sep := range []string{" feat. ", " ft. ", " featuring ", " feat ", " ft ", ", ", "; ", " & ", " × ", " and "} {
		if i := strings.Index(lower, strings.ToLower(sep)); i > 0 {
			a = a[:i]
			lower = strings.ToLower(a)
		}
	}
	a = stripBrackets(a)
	a = pipeTail.ReplaceAllString(a, "")

	return strings.TrimSpace(a)
}

// scrub removes the fixed decorations and a trailing official marker.
func scrub(s string) string {
	for _, w := range junkWords {
		s = strings.ReplaceAll(s, w, "")
	}
	s = officialCase.ReplaceAllString(s, "")

	return strings.TrimSpace(s)
}

// stripBrackets cuts the marketing brackets off a title, the repeated kind as
// well: "Song (Official MV) (HD)" loses both in one loop.
func stripBrackets(s string) string {
	prev := ""
	for prev != s {
		prev = s
		s = bracketJunk.ReplaceAllString(s, " ")
		s = yearBracket.ReplaceAllString(s, "")
	}
	s = featTail.ReplaceAllString(s, "")

	return strings.TrimSpace(s)
}
