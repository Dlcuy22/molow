package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A video-category track lists only its feed thumbnails (hqdefault at best,
// letterboxed 480x360), while the host keeps larger stills the listing never
// mentions. thumbCandidates orders the walk with the large stills first and
// the listed URL last; song artwork and foreign URLs pass through untouched.
func TestThumbCandidates(t *testing.T) {
	cases := []struct {
		url  string
		want []string
	}{
		{
			"https://i.ytimg.com/vi/ARqP5ohiubY/hqdefault.jpg?sqp=x",
			[]string{
				"https://i.ytimg.com/vi/ARqP5ohiubY/maxresdefault.jpg?sqp=x",
				"https://i.ytimg.com/vi/ARqP5ohiubY/sddefault.jpg?sqp=x",
				"https://i.ytimg.com/vi/ARqP5ohiubY/hqdefault.jpg?sqp=x",
			},
		},
		{
			"https://i.ytimg.com/vi/ARqP5ohiubY/hq720.jpg",
			[]string{
				"https://i.ytimg.com/vi/ARqP5ohiubY/maxresdefault.jpg",
				"https://i.ytimg.com/vi/ARqP5ohiubY/sddefault.jpg",
				"https://i.ytimg.com/vi/ARqP5ohiubY/hq720.jpg",
			},
		},
		{
			"https://yt3.googleusercontent.com/jPyn9=w720-h720-l90-rj",
			[]string{"https://yt3.googleusercontent.com/jPyn9=w720-h720-l90-rj"},
		},
		{"", []string{""}},
	}
	for _, c := range cases {
		got := thumbCandidates(c.url)
		if len(got) != len(c.want) {
			t.Fatalf("thumbCandidates(%q) = %v, want %v", c.url, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("thumbCandidates(%q) = %v, want %v", c.url, got, c.want)
			}
		}
	}
}

// TestThumbForFallsThroughTheCandidates pins the walk itself: the first
// candidate that downloads wins, a missing large still (the 404 the host
// answers for videos without one) moves to the next, and the listed URL
// remains the floor.
func TestThumbForFallsThroughTheCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vi/ARqP5ohiubY/maxresdefault.jpg":
			w.WriteHeader(http.StatusNotFound)
		case "/vi/ARqP5ohiubY/sddefault.jpg":
			_, _ = w.Write([]byte("sd-still"))
		case "/vi/EXxQmcbI-Bs/maxresdefault.jpg":
			_, _ = w.Write([]byte("maxres-still"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/vi/"
	p := &ytmProvider{http: srv.Client()}

	if data := p.thumbFor(t.Context(), base+"ARqP5ohiubY/hqdefault.jpg"); string(data) != "sd-still" {
		t.Fatalf("thumb = %q, want the sddefault fallthrough", data)
	}
	if data := p.thumbFor(t.Context(), base+"EXxQmcbI-Bs/hqdefault.jpg"); string(data) != "maxres-still" {
		t.Fatalf("thumb = %q, want the maxresdirect hit", data)
	}
}
