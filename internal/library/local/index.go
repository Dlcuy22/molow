package local

import (
	"cmp"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Matches disc+track prefixes: "1-01. Song", "2-03 Song", "CD1-02 Song",
// "Disc 2 - 03 Song", "D1T02 Song".
var reDiscTrack = regexp.MustCompile(
	`(?i)^(?:(?:cd|disc|disk)\s*0*(\d{1,2})\s*[-._\s]+\s*(?:(?:track|t)\s*)?0*(\d{1,3})|d0*(\d{1,2})t0*(\d{1,3})|0*(\d{1,2})-0*(\d{1,3}))(?:\s*[.)]\s*(?:[-–—_]\s*)?|\s*[-–—_]\s*|\s+|$)`,
)

// Matches track prefixes: "01. Song", "1 Song", "01 Song", "01 - Song",
// "01_Song", "(01) Song", "[01] Song", "01) Song", "#01 Song", "Track 01 Song", "01".
var reTrack = regexp.MustCompile(
	`(?i)^(?:(?:track|no\.?|#)\s*0*(\d{1,3})(?:\s*[.)]\s*(?:[-–—_]\s*)?|\s*[-–—_]\s*|\s+|$)|[\(\[]0*(\d{1,3})[\)\]]\s*(?:[.\-–—_]\s*)?|0*(\d{1,3})(?:\s*[.)]\s*(?:[-–—_]\s*)?|\s*[-–—_]\s*|\s+|$))`,
)

// parseTrackIndex extracts a leading disc/track index and remaining title from
// a file path's stem.
func parseTrackIndex(path string) (disc, track int, rest string, ok bool) {
	stem := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if stem == "" {
		return 0, 0, "", false
	}

	if m := reDiscTrack.FindStringSubmatchIndex(stem); m != nil {
		d, t := firstPair(stem, m, [][2]int{{2, 4}, {6, 8}, {10, 12}})
		if d > 0 && t > 0 {
			tail := strings.TrimSpace(stem[m[1]:])
			if !isDecimalPrefix(stem[:m[1]], tail) {
				return d, t, tail, true
			}
		}
	}

	if m := reTrack.FindStringSubmatchIndex(stem); m != nil {
		t := firstInt(stem, m, []int{2, 4, 6})
		if t > 0 {
			tail := strings.TrimSpace(stem[m[1]:])
			if !isDecimalPrefix(stem[:m[1]], tail) {
				return 0, t, tail, true
			}
		}
	}

	return 0, 0, stem, false
}

// isDecimalPrefix rejects "3.14 Pi" where a dot separates digits instead of a title.
func isDecimalPrefix(matched, tail string) bool {
	return strings.HasSuffix(matched, ".") && len(tail) > 0 && tail[0] >= '0' && tail[0] <= '9'
}

func firstPair(s string, loc []int, pairs [][2]int) (int, int) {
	for _, p := range pairs {
		if loc[p[0]] >= 0 && loc[p[1]] >= 0 {
			a, _ := strconv.Atoi(s[loc[p[0]]:loc[p[0]+1]])
			b, _ := strconv.Atoi(s[loc[p[1]]:loc[p[1]+1]])

			return a, b
		}
	}

	return 0, 0
}

func firstInt(s string, loc []int, slots []int) int {
	for _, slot := range slots {
		if loc[slot] >= 0 {
			n, _ := strconv.Atoi(s[loc[slot]:loc[slot+1]])

			return n
		}
	}

	return 0
}

type pathKey struct {
	path   string
	dirOrd int
	hasIdx bool
	disc   int
	track  int
	lower  string
}

// sortPathsByIndex orders files numerically within each directory when filename
// indices are present, while preserving directory encounter order across folders.
func sortPathsByIndex(paths []string) {
	if len(paths) < 2 {
		return
	}
	dirOrder := make(map[string]int)
	keys := make([]pathKey, len(paths))
	for i, p := range paths {
		dir := filepath.Dir(p)
		ord, seen := dirOrder[dir]
		if !seen {
			ord = len(dirOrder)
			dirOrder[dir] = ord
		}
		disc, track, _, ok := parseTrackIndex(p)
		if ok && disc == 0 {
			disc = 1
		}
		base := filepath.Base(p)
		keys[i] = pathKey{
			path:   p,
			dirOrd: ord,
			hasIdx: ok,
			disc:   disc,
			track:  track,
			lower:  strings.ToLower(base),
		}
	}

	slices.SortStableFunc(keys, func(a, b pathKey) int {
		if c := cmp.Compare(a.dirOrd, b.dirOrd); c != 0 {
			return c
		}
		if a.hasIdx != b.hasIdx {
			if a.hasIdx {
				return -1
			}

			return 1
		}
		if a.hasIdx {
			if c := cmp.Compare(a.disc, b.disc); c != 0 {
				return c
			}
			if c := cmp.Compare(a.track, b.track); c != 0 {
				return c
			}
		}

		return cmp.Compare(a.lower, b.lower)
	})

	for i, k := range keys {
		paths[i] = k.path
	}
}
