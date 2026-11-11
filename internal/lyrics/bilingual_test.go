package lyrics

import "testing"

// The bilingual compound a YouTube Music track carries ("JP - EN") is the
// title the resolvers search, but the Japanese catalogue stores the bare JP
// form. The split must fire on a spaced dash the same way it does on the slash
// forms, or the catalogue lookups miss.
func TestAliasesSplitsTheBilingualDash(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"アドレナ - ADRENA", []string{"アドレナ", "ADRENA"}},
		{"過去に囚われている - Trapped in the past", []string{"過去に囚われている", "Trapped in the past"}},
		{"いますぐ輪廻 – Retry Now", []string{"いますぐ輪廻", "Retry Now"}}, // en dash
		{"いますぐ輪廻 — Retry Now", []string{"いますぐ輪廻", "Retry Now"}}, // em dash
		{"いますぐ輪廻 / Retry Now", []string{"いますぐ輪廻", "Retry Now"}}, // the slash form, unchanged
	}
	for _, c := range cases {
		got := Aliases(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("Aliases(%q) = %q, want %q", c.in, got, c.want)
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Fatalf("Aliases(%q) = %q, want %q", c.in, got, c.want)
			}
		}
	}
}

// The search candidates keep the full compound first, since the betterlyrics
// search needs it to score over its threshold, with the split parts behind it.
func TestTitleAliasesLeadsWithTheCompound(t *testing.T) {
	got := titleAliases(TrackMeta{Title: "アドレナ - ADRENA"})
	want := []string{"アドレナ - ADRENA", "アドレナ", "ADRENA"}
	if len(got) != len(want) {
		t.Fatalf("titleAliases = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("titleAliases = %q, want %q", got, want)
		}
	}
}
