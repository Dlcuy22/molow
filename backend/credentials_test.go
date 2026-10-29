package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
)

// isolateConfig points the config directory at a temporary home, so a test can
// never touch the real credentials file.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestSaveCredentialsIsPrivateAndAtomic(t *testing.T) {
	isolateConfig(t)

	creds := &Credentials{
		CookieHeader: "SAPISID=a; SID=b",
		Name:         "Test User",
		Email:        "test@example.invalid",
	}
	if err := saveCredentials(creds); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	// A leftover temporary file would mean the write was not atomic, and it would
	// also be a second copy of the secrets on disk.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestLoadCredentialsRoundTrip(t *testing.T) {
	isolateConfig(t)

	want := &Credentials{
		CookieHeader: "SAPISID=a; SID=b; LOGIN_INFO=c",
		VisitorData:  "visitor",
		AuthUser:     "3",
		DataSyncID:   "123||",
		ChannelID:    "UCchannel",
		Name:         "Test User",
		Email:        "test@example.invalid",
		Cookies: []Cookie{
			{Name: "SAPISID", Value: "a", Domain: ".youtube.com", Path: "/", HTTPOnly: true},
		},
	}
	if err := saveCredentials(want); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	got, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if got.CookieHeader != want.CookieHeader {
		t.Errorf("CookieHeader = %q", got.CookieHeader)
	}
	if got.AuthUser != "3" {
		t.Errorf("AuthUser = %q, want 3", got.AuthUser)
	}
	if got.DataSyncID != "123||" {
		t.Errorf("DataSyncID = %q", got.DataSyncID)
	}
	if len(got.Cookies) != 1 || !got.Cookies[0].HTTPOnly {
		t.Errorf("Cookies = %+v, want one HttpOnly entry", got.Cookies)
	}
	// CapturedAt is filled in on save, so the UI can show when the session began.
	if got.CapturedAt.IsZero() {
		t.Error("CapturedAt is zero")
	}
}

func TestLoadCredentialsMissing(t *testing.T) {
	isolateConfig(t)

	_, err := loadCredentials()
	if err != ErrNoCredentials {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
}

func TestLoadCredentialsRejectsWrongVersion(t *testing.T) {
	isolateConfig(t)

	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"cookie_header":"SAPISID=a; SID=b"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := loadCredentials(); err == nil {
		t.Fatal("a file with an unknown version was accepted")
	}
}

func TestClearCredentials(t *testing.T) {
	isolateConfig(t)

	if err := clearCredentials(); err != nil {
		t.Fatalf("clearing a missing file should not fail: %v", err)
	}

	if err := saveCredentials(&Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	if err := clearCredentials(); err != nil {
		t.Fatalf("clearCredentials: %v", err)
	}
	if _, err := loadCredentials(); err != ErrNoCredentials {
		t.Errorf("error = %v, want ErrNoCredentials after clearing", err)
	}
}

func TestNormalizeCookieHeader(t *testing.T) {
	cases := map[string]string{
		"  SAPISID=a; SID=b  ":     "SAPISID=a; SID=b",
		"Cookie: SAPISID=a; SID=b": "SAPISID=a; SID=b",
		"cookie:SAPISID=a; SID=b":  "SAPISID=a; SID=b",
		"SAPISID=a;\nSID=b":        "SAPISID=a; SID=b",
		"SAPISID=a; SID=b;":        "SAPISID=a; SID=b",
		"":                         "",
	}

	for in, want := range cases {
		if got := normalizeCookieHeader(in); got != want {
			t.Errorf("normalizeCookieHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckCookieHeader(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		wantErr string
	}{
		{
			name:    "empty",
			header:  "",
			wantErr: "empty",
		},
		{
			// The shape measured live: a real SAPISID that authenticates nothing.
			name:    "seed without identity",
			header:  "SAPISID=abc",
			wantErr: "no SID",
		},
		{
			name:    "identity without seed",
			header:  "SID=abc; HSID=def",
			wantErr: "no SAPISID",
		},
		{
			name:   "complete session",
			header: "SAPISID=abc; SID=def; LOGIN_INFO=x",
		},
		{
			name:   "secure variants",
			header: "__Secure-3PAPISID=abc; __Secure-3PSID=def",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCookieHeader(tc.header)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestClientFromCredentialsAppliesChannel(t *testing.T) {
	creds := &Credentials{
		CookieHeader: "SAPISID=abc; SID=def",
		VisitorData:  "visitor",
		AuthUser:     "2",
		DataSyncID:   "sync||",
		ChannelID:    "UCchannel",
	}

	client, err := clientFromCredentials(creds)
	if err != nil {
		t.Fatalf("clientFromCredentials: %v", err)
	}
	if client == nil {
		t.Fatal("nil client")
	}

	// The channel has to reach the session, or a brand account would always
	// behave like its first channel.
	auth := client.Auth()
	if auth == nil {
		t.Fatal("client has no auth")
	}
	if auth.AuthUser != "2" {
		t.Errorf("AuthUser = %q, want 2", auth.AuthUser)
	}
	if auth.ChannelID != "UCchannel" {
		t.Errorf("ChannelID = %q", auth.ChannelID)
	}
	if auth.DataSyncID != "sync||" {
		t.Errorf("DataSyncID = %q", auth.DataSyncID)
	}
}

func TestClientFromCredentialsRejectsEmpty(t *testing.T) {
	if _, err := clientFromCredentials(nil); err == nil {
		t.Error("nil credentials were accepted")
	}
	if _, err := clientFromCredentials(&Credentials{}); err == nil {
		t.Error("credentials with no cookie header were accepted")
	}
}

func TestCredentialsFromLoginKeepsDomainAndPath(t *testing.T) {
	// The header throws domain and path away; Restore needs them back, so the
	// conversion has to carry them across.
	result := loginwinResultForTest()
	creds := credentialsFromLogin(result)

	if creds.CookieHeader != "SAPISID=abc; SID=def" {
		t.Errorf("CookieHeader = %q", creds.CookieHeader)
	}
	if len(creds.Cookies) != 2 {
		t.Fatalf("got %d cookies, want 2", len(creds.Cookies))
	}
	if creds.Cookies[0].Domain != ".youtube.com" || creds.Cookies[0].Path != "/" {
		t.Errorf("cookie lost its scope: %+v", creds.Cookies[0])
	}
	if !creds.Cookies[0].HTTPOnly {
		t.Error("HttpOnly was not carried across")
	}
	if !creds.Cookies[0].Expires.After(time.Now()) {
		t.Error("expiry was not carried across")
	}

	// And back again, for Restore.
	back := creds.loginCookies()
	if len(back) != 2 || back[1].Name != "SID" {
		t.Errorf("loginCookies = %+v", back)
	}
}

// loginwinResultForTest builds the result a successful capture produces, so the
// conversion can be checked without opening a window.
func loginwinResultForTest() loginwin.Result {
	expires := time.Now().Add(24 * time.Hour)
	return loginwin.Result{
		URL:          "https://music.youtube.com/",
		SAPISID:      "abc",
		CookieHeader: "SAPISID=abc; SID=def",
		Cookies: []loginwin.Cookie{
			{Name: "SAPISID", Value: "abc", Domain: ".youtube.com", Path: "/", HTTPOnly: true, Expires: expires},
			{Name: "SID", Value: "def", Domain: ".youtube.com", Path: "/", Secure: true, Expires: expires},
		},
	}
}
