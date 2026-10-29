package loginwin

import (
	"strings"
	"testing"

	ytm "github.com/dlcuy22/ytm-go"
)

func TestCookieHeaderPrefersTheMusicHost(t *testing.T) {
	// The same name in two scopes. GenerateSAPISIDHash hashes against
	// https://music.youtube.com, so the exact-host value is the one that works,
	// and the result must not depend on input order.
	order := [][]Cookie{
		{
			{Name: "SAPISID", Value: "from-google", Domain: ".google.com", Path: "/"},
			{Name: "SAPISID", Value: "from-music", Domain: "music.youtube.com", Path: "/"},
		},
		{
			{Name: "SAPISID", Value: "from-music", Domain: "music.youtube.com", Path: "/"},
			{Name: "SAPISID", Value: "from-google", Domain: ".google.com", Path: "/"},
		},
	}

	for i, cookies := range order {
		got := cookieHeader(cookies)
		if !strings.Contains(got, "SAPISID=from-music") {
			t.Errorf("order %d: header = %q, want the music.youtube.com value", i, got)
		}
		if strings.Count(got, "SAPISID=") != 1 {
			t.Errorf("order %d: header carries SAPISID more than once: %q", i, got)
		}
	}
}

func TestCookieHeaderDropsForeignAndPathScopedCookies(t *testing.T) {
	got := cookieHeader([]Cookie{
		{Name: "LOGIN_INFO", Value: "keep", Domain: ".youtube.com", Path: "/"},
		{Name: "OAUTH", Value: "drop", Domain: "accounts.google.com", Path: "/"},
		{Name: "YOUTUBEI", Value: "drop", Domain: ".youtube.com", Path: "/youtubei"},
	})

	if !strings.Contains(got, "LOGIN_INFO=keep") {
		t.Errorf("header = %q, want LOGIN_INFO kept", got)
	}
	if strings.Contains(got, "OAUTH") {
		t.Errorf("header = %q, want the accounts.google.com cookie dropped", got)
	}
	if strings.Contains(got, "YOUTUBEI") {
		t.Errorf("header = %q, want the path-scoped cookie dropped", got)
	}
}

func TestCookieHeaderCollapsesDuplicates(t *testing.T) {
	got := cookieHeader([]Cookie{
		{Name: "SID", Value: "a", Domain: ".google.com", Path: "/"},
		{Name: "SID", Value: "b", Domain: ".youtube.com", Path: "/"},
		{Name: "SID", Value: "c", Domain: "music.youtube.com", Path: "/"},
		{Name: "SAPISID", Value: "s", Domain: ".youtube.com", Path: "/"},
	})

	// Compare by exact name: a substring count would also match SAPISID.
	values := map[string][]string{}
	for _, part := range strings.Split(got, "; ") {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		values[name] = append(values[name], value)
	}

	if n := len(values["SID"]); n != 1 {
		t.Errorf("SID appears %d times in %q, want 1", n, got)
	}
	if values["SID"][0] != "c" {
		t.Errorf("SID = %q, want the music host value", values["SID"][0])
	}
	if n := len(values["SAPISID"]); n != 1 {
		t.Errorf("SAPISID appears %d times in %q, want 1", n, got)
	}
}

func TestCookieHeaderEmpty(t *testing.T) {
	got := cookieHeader(nil)
	if got != "" {
		t.Fatalf("header = %q, want empty", got)
	}
	// An empty header must fail loudly at the auth step rather than produce a
	// client that fails later with an empty library.
	if _, err := ytm.NewAuthFromCookieString(got); err == nil {
		t.Error("NewAuthFromCookieString accepted an empty header")
	}
}

func TestCookieHeaderSkipsEmptyNames(t *testing.T) {
	got := cookieHeader([]Cookie{
		{Name: "", Value: "orphan", Domain: ".youtube.com", Path: "/"},
		{Name: "SID", Value: "ok", Domain: ".youtube.com", Path: "/"},
	})
	if got != "SID=ok" {
		t.Errorf("header = %q, want only SID", got)
	}
}

func TestHasSessionCookies(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{
			// The shape measured live: a real SAPISID with no session identity
			// authenticates nothing.
			name:   "seed without identity",
			header: "SAPISID=abc",
			want:   false,
		},
		{
			name:   "identity without seed",
			header: "SID=abc; HSID=def",
			want:   false,
		},
		{
			name:   "seed and identity",
			header: "SAPISID=abc; SID=def; LOGIN_INFO=x",
			want:   true,
		},
		{
			name:   "secure variants",
			header: "__Secure-3PAPISID=abc; __Secure-3PSID=def",
			want:   true,
		},
		{
			name:   "empty",
			header: "",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasSessionCookies(tc.header); got != tc.want {
				t.Errorf("HasSessionCookies(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestSapiSIDPrefersExactHost(t *testing.T) {
	got := sapiSID([]Cookie{
		{Name: "SAPISID", Value: "google", Domain: ".google.com", Path: "/"},
		{Name: "SAPISID", Value: "youtube", Domain: ".youtube.com", Path: "/"},
		{Name: "SAPISID", Value: "music", Domain: "music.youtube.com", Path: "/"},
	})
	if got != "music" {
		t.Errorf("sapiSID = %q, want the music host value", got)
	}
}

func TestDomainPriority(t *testing.T) {
	cases := map[string]int{
		"music.youtube.com":   priorityExactHost,
		".youtube.com":        priorityYouTube,
		".google.com":         priorityGoogle,
		"accounts.google.com": priorityNone,
		"example.com":         priorityNone,
	}
	for domain, want := range cases {
		if got := domainPriority(domain); got != want {
			t.Errorf("domainPriority(%q) = %d, want %d", domain, got, want)
		}
	}
}
