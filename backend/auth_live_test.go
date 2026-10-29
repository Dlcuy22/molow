package backend

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ytm "github.com/dlcuy22/ytm-go"
)

// Live checks against a real signed-in account. They are opt-in because they
// need credentials and the network:
//
//	MOLOW_LIVE_COOKIE_FILE=.env go test ./backend -run Live -v
//
// The file may be either a plain env file with BURNER_COOKIE=... or a file
// holding the raw header on its own line. No test here prints a cookie value.

// liveCookieHeader reads a cookie header out of a file, accepting both the env
// file shape and a bare header.
func liveCookieHeader(t *testing.T) string {
	t.Helper()
	path := os.Getenv("MOLOW_LIVE_COOKIE_FILE")
	if path == "" {
		t.Skip("set MOLOW_LIVE_COOKIE_FILE to a file holding a signed-in cookie header")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	header := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			switch strings.TrimSpace(key) {
			case "BURNER_COOKIE", "MOLOW_COOKIE", "COOKIE":
				header = strings.TrimSpace(value)
				continue
			}
		}
		// A bare header line: it has cookie pairs but no env key.
		if header == "" && strings.Contains(line, "=") && strings.Contains(line, ";") {
			header = line
		}
	}

	header = normalizeCookieHeader(header)
	if header == "" {
		t.Fatalf("%s holds no cookie header", path)
	}
	return header
}

func TestLiveAuthPipeline(t *testing.T) {
	header := liveCookieHeader(t)

	// The header must pass the pre-flight check before anything is stored.
	if err := checkCookieHeader(header); err != nil {
		t.Fatalf("the captured header does not look like a session: %v", err)
	}

	creds := &Credentials{CookieHeader: header, CapturedAt: time.Now()}
	client, err := clientFromCredentials(creds)
	if err != nil {
		t.Fatalf("clientFromCredentials: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The five authed readers, which the plan requires to return non-zero counts
	// rather than an empty list.
	type reader struct {
		name string
		run  func() (int, error)
	}
	readers := []reader{
		{"GetLikedSongs", func() (int, error) {
			v, _, err := client.GetLikedSongs(ctx)
			return len(v), err
		}},
		{"GetAccountPlaylists", func() (int, error) {
			v, err := client.GetAccountPlaylists(ctx)
			return len(v), err
		}},
		{"GetLikedArtists", func() (int, error) {
			v, err := client.GetLikedArtists(ctx)
			return len(v), err
		}},
		{"GetLikedAlbums", func() (int, error) {
			v, err := client.GetLikedAlbums(ctx)
			return len(v), err
		}},
		{"GetLibraryPlaylists", func() (int, error) {
			v, _, err := client.GetLibraryPlaylists(ctx)
			return len(v), err
		}},
	}

	anyNonEmpty := false
	for _, r := range readers {
		n, err := r.run()
		if err != nil {
			if err == ytm.ErrSessionExpired {
				t.Fatalf("%s: the session is dead; capture a fresh cookie", r.name)
			}
			t.Errorf("%s: %v", r.name, err)
			continue
		}
		t.Logf("%-22s items=%d", r.name, n)
		if n > 0 {
			anyNonEmpty = true
		}
	}
	if !anyNonEmpty {
		t.Error("every reader returned zero; the session authenticates nothing")
	}

	// The identity endpoints, which are what the UI shows.
	accounts, err := client.GetAccounts(ctx)
	if err != nil {
		t.Errorf("GetAccounts: %v", err)
	} else {
		t.Logf("accounts=%d", len(accounts))
		for _, a := range accounts {
			t.Logf("  channel=%q handle=%q pageID=%q authUser=%q selected=%v",
				a.Name, a.ChannelHandle, a.PageID, a.AuthUser, a.IsSelected)
		}
	}

	info, err := client.GetAccountInfo(ctx)
	if err != nil {
		t.Errorf("GetAccountInfo: %v", err)
	} else {
		t.Logf("account name=%q email=%q", info.Name, info.Email)
	}

	if !client.SignedIn() {
		t.Error("SignedIn() = false after a successful authed read")
	}
}

func TestLiveSessionExpiryIsDetected(t *testing.T) {
	// A header with a real SAPISID but no primary session cookie is the shape
	// measured to return the signed-out page. The client must report it as an
	// expired session rather than an empty library.
	header := liveCookieHeader(t)

	var sapisid string
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "SAPISID=") {
			sapisid = part
		}
	}
	if sapisid == "" {
		t.Skip("the captured header has no SAPISID to strip down to")
	}

	client, err := clientFromCredentials(&Credentials{CookieHeader: sapisid})
	if err != nil {
		t.Fatalf("clientFromCredentials: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, _, err = client.GetLikedSongs(ctx)
	if err != ytm.ErrSessionExpired {
		t.Fatalf("error = %v, want ErrSessionExpired for a SAPISID-only header", err)
	}
}
