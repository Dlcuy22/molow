package romanize

import (
	"strings"
	"testing"
)

func TestParts(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{name: "hiragana", in: []string{"なみだ"}, want: "namida"},
		{name: "katakana folds to hiragana", in: []string{"ヴァンパイア"}, want: "vampaia"},
		{name: "digraph wins over two runes", in: []string{"きゃく"}, want: "kyaku"},
		{name: "small tsu doubles across parts", in: []string{"マッ", "テ"}, want: "matte"},
		{name: "small tsu before ch", in: []string{"スッ", "チャッ"}, want: "sutcha"},
		{name: "long vowel mark reaches back", in: []string{"モー"}, want: "moo"},
		{name: "long vowel mark across parts", in: []string{"モ", "ー"}, want: "moo"},
		{name: "long vowel mark does not leak across nasal", in: []string{"たんー"}, want: "tan"},
		{name: "long vowel mark does not leak across space", in: []string{"hello ー"}, want: "hello "},
		{name: "nasal before a vowel", in: []string{"んあ"}, want: "n'a"},
		{name: "nasal before y", in: []string{"んや"}, want: "n'ya"},
		{name: "nasal before b", in: []string{"んば"}, want: "mba"},
		{name: "nasal before p", in: []string{"んぱ"}, want: "mpa"},
		{name: "nasal before a consonant", in: []string{"んた"}, want: "nta"},
		{name: "particle を", in: []string{"を"}, want: "o"},
		{name: "punctuation passes through", in: []string{"だって = あいたく"}, want: "datte = aitaku"},
		{name: "latin passes through", in: []string{"hello"}, want: "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(Parts(tc.in), ""); got != tc.want {
				t.Fatalf("Parts(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPartsKeepsOneStringPerPart(t *testing.T) {
	got := Parts([]string{"なみ", "だ"})
	if len(got) != 2 {
		t.Fatalf("Parts returned %d parts, want 2", len(got))
	}
	if got[0] != "nami" || got[1] != "da" {
		t.Fatalf("Parts = %q, want [nami da]", got)
	}
}
