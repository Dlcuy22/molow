package lyrics

import "testing"

func TestAliasesSplitsDualLanguage(t *testing.T) {
	got := Aliases("なとり / natori")
	if len(got) != 2 || got[0] != "なとり" || got[1] != "natori" {
		t.Fatalf("aliases: %v", got)
	}
	if got := Aliases("Kenshi Yonezu  米津玄師"); len(got) != 2 {
		t.Fatalf("double-space aliases: %v", got)
	}
	if got := Aliases("Kenshi Yonezu"); len(got) != 1 || got[0] != "Kenshi Yonezu" {
		t.Fatalf("plain alias: %v", got)
	}
}

func TestAliasesStripsChannelArtefacts(t *testing.T) {
	if got := Aliases("Song - Topic"); len(got) != 1 || got[0] != "Song" {
		t.Fatalf("' - Topic': %v", got)
	}
	if got := Aliases("ArtistVEVO"); len(got) != 1 || got[0] != "Artist" {
		t.Fatalf("VEVO: %v", got)
	}
}

func TestCleanKeyStripsBracketJunk(t *testing.T) {
	cases := map[string]string{
		"Song (Official MV)":                    "Song",
		"Song (Official Video) (HD)":            "Song",
		"Song [Lyrics]":                         "Song",
		"Song (feat. Other)":                    "Song",
		"Song feat. Other":                      "Song",
		"Song (2020)":                           "Song",
		"Song | Official Channel":               "Song",
		"Song: feat. Other":                     "Song:",
		"Song (Official Music Video) [4K] (MV)": "Song",
		// Content that disambiguates a release stays.
		"Song (Live at Budokan)": "Song (Live at Budokan)",
	}
	for in, want := range cases {
		if got := CleanKey(in); got != want {
			t.Fatalf("CleanKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilenameIsSafe(t *testing.T) {
	got := Filename("AC/DC: Best <of> \"Hits\" *Vol. 1?* (Official)")
	if got != "AC DC  Best  of   Hits   Vol. 1" {
		t.Fatalf("Filename: %q", got)
	}
}

func TestPrimaryArtist(t *testing.T) {
	cases := map[string]string{
		"A & B":                  "A",
		"A, B":                   "A",
		"A feat. B":              "A",
		"A ft. B":                "A",
		"A featuring B":          "A",
		"A×B":                    "A×B",
		"A - Topic":              "A",
		"なとり / natori (feat. X)": "なとり / natori",
		// The channel-title tails: the marker goes, the artist keeps its
		// last letter. A character guard here used to cut "Rammstein" into
		// "Rammstei" and the providers searched for nothing.
		"Rammstein Official":                 "Rammstein",
		"Nirvana Official":                   "Nirvana",
		"Rammstein official YouTube channel": "Rammstein",
		"Song (Official MV) Official":        "Song",
		"A Official Video":                   "A",
		"A Official":                         "A",
		"Official":                           "Official",
	}
	for in, want := range cases {
		if got := PrimaryArtist(in); got != want {
			t.Fatalf("PrimaryArtist(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQualityTiers(t *testing.T) {
	cases := map[string]int{
		"":                               0,
		"plain text only":                1,
		"[00:01.00]line after line":      2,
		"[00:01.00]line\n<line:1:1.2>":   3,
		"[00:01.00]<00:01.0>word<00:02>": 3,
	}
	for src, want := range cases {
		if got := Quality(src); got != want {
			t.Fatalf("Quality(%q) = %d, want %d", src, got, want)
		}
	}
}
