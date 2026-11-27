package library

import "testing"

func TestScopedRemoteRefAndParseRemoteRef(t *testing.T) {
	if got := RemoteRef(KindYTM, "vid1"); got != "ytm:vid1" {
		t.Fatalf("RemoteRef = %q", got)
	}
	if got := ScopedRemoteRef(KindYTM, "", "vid1"); got != "ytm:vid1" {
		t.Fatalf("ScopedRemoteRef empty lib = %q", got)
	}
	if got := ScopedRemoteRef(KindYTM, "ytm_a1", "vid1"); got != "ytm:ytm_a1:vid1" {
		t.Fatalf("ScopedRemoteRef = %q", got)
	}

	cases := []struct {
		ref    string
		wantOK bool
		kind   Kind
		libID  string
		native string
	}{
		{ref: "ytm:ytm_a1:vid123", wantOK: true, kind: KindYTM, libID: "ytm_a1", native: "vid123"},
		{ref: "subsonic:srv1:tr_42", wantOK: true, kind: Kind("subsonic"), libID: "srv1", native: "tr_42"},
		{ref: "ytm:vid123", wantOK: true, kind: KindYTM, libID: "", native: "vid123"},
		{ref: `C:\Music\song.flac`, wantOK: false},
		{ref: `/home/user/song.flac`, wantOK: false},
		{ref: "local:foo", wantOK: false},
		{ref: "https://music.youtube.com/watch?v=1", wantOK: false},
		{ref: "http://example.com/a.mp3", wantOK: false},
		{ref: "file:///a.mp3", wantOK: false},
		{ref: "ytm::vid1", wantOK: false},
		{ref: "ytm:lib1:", wantOK: false},
		{ref: "ytm:", wantOK: false},
		{ref: "", wantOK: false},
	}

	for _, tc := range cases {
		kind, libID, native, ok := ParseRemoteRef(tc.ref)
		if ok != tc.wantOK || kind != tc.kind || libID != tc.libID || native != tc.native {
			t.Errorf("ParseRemoteRef(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
				tc.ref, kind, libID, native, ok, tc.kind, tc.libID, tc.native, tc.wantOK)
		}
	}
}
