package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
)

// memKeyStore is an in-memory KeyStore so unit tests exercise envelope
// encryption without ever touching the user's real OS keyring.
type memKeyStore struct {
	mu       sync.Mutex
	key      []byte
	probeErr error
}

func newMemKeyStore() *memKeyStore {
	return &memKeyStore{}
}

func (m *memKeyStore) Probe() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.probeErr
}

func (m *memKeyStore) GetKey() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.key) == 0 {
		return nil, ErrKeyNotFound
	}
	cp := make([]byte, len(m.key))
	copy(cp, m.key)
	return cp, nil
}

func (m *memKeyStore) GetOrCreateKey() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probeErr != nil {
		return nil, m.probeErr
	}
	if len(m.key) == 0 {
		m.key = make([]byte, aes256KeySize)
		if _, err := rand.Read(m.key); err != nil {
			return nil, err
		}
	}
	cp := make([]byte, len(m.key))
	copy(cp, m.key)
	return cp, nil
}

func (m *memKeyStore) DeleteKey() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.key = nil
	return nil
}

// isolateConfig points the config directory at a temporary home and installs
// an in-memory keyring, so a test can never touch the real credentials file or
// real OS keyring.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	setKeyStoreForTest(t, newMemKeyStore())
	return dir
}

func TestSaveCredentialsIsPrivateAndAtomic(t *testing.T) {
	isolateConfig(t)

	creds := &Credentials{
		CookieHeader: "SAPISID=secret-seed-value; SID=secret-sid-value",
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
	if !strings.HasSuffix(path, "credentials.enc") {
		t.Fatalf("credentialsPath = %q, want credentials.enc", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 600", perm)
		}
	}

	// On-disk ciphertext must never expose plaintext cookie strings or values.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	for _, needle := range []string{"SAPISID", "secret-seed-value", "SID", "secret-sid-value", "Test User"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("credentials.enc contains plaintext %q", needle)
		}
	}

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
	if got.CapturedAt.IsZero() {
		t.Error("CapturedAt is zero")
	}
}

func TestLoadCredentialsMissingAndKeyErrors(t *testing.T) {
	isolateConfig(t)

	if _, err := loadCredentials(); err != ErrNoCredentials {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}

	if err := saveCredentials(&Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	// Missing key in keyring -> ErrNoCredentials (no plaintext fallback).
	if err := getKeyStore().DeleteKey(); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, err := loadCredentials(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("loadCredentials with missing key = %v, want ErrNoCredentials", err)
	}
}

func TestLoadCredentialsRejectsTamperedOrWrongEnvelope(t *testing.T) {
	isolateConfig(t)

	if err := saveCredentials(&Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"cookie_header":"SAPISID=a; SID=b"}`), 0o600); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("unencrypted plaintext file was accepted")
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
	if _, err := getKeyStore().GetKey(); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("keyring GetKey = %v, want ErrKeyNotFound after clearCredentials", err)
	}
}

func TestSignInAbortsWhenKeyringUnavailable(t *testing.T) {
	isolateConfig(t)
	ks := &memKeyStore{probeErr: errors.New("org.freedesktop.secrets unavailable")}
	setKeyStoreForTest(t, ks)

	svc := &PlayerService{}
	if err := svc.CheckKeyring(); err == nil {
		t.Fatal("CheckKeyring succeeded when keyring is unavailable")
	}
	if err := svc.SignIn(); err == nil || !strings.Contains(err.Error(), "org.freedesktop.secrets") {
		t.Fatalf("SignIn err = %v, want keyring failure", err)
	}
	if err := svc.SignInWithCookieHeader("SAPISID=abc; SID=def"); err == nil || !strings.Contains(err.Error(), "org.freedesktop.secrets") {
		t.Fatalf("SignInWithCookieHeader err = %v, want keyring failure", err)
	}

	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file created despite failed keyring pre-flight: %v", err)
	}
	_ = context.Background()
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

func TestCredentialsFromLoginMapsHeaderAndMetadata(t *testing.T) {
	result := loginwin.Result{
		URL:          "https://music.youtube.com/",
		SAPISID:      "abc",
		CookieHeader: "SAPISID=abc; SID=def",
		VisitorData:  "visitor",
		AuthUser:     "1",
		DataSyncID:   "sync||",
	}
	creds := credentialsFromLogin(result)

	if creds.CookieHeader != "SAPISID=abc; SID=def" {
		t.Errorf("CookieHeader = %q", creds.CookieHeader)
	}
	if creds.VisitorData != "visitor" || creds.AuthUser != "1" || creds.DataSyncID != "sync||" {
		t.Errorf("unexpected metadata: %+v", creds)
	}
	if creds.CapturedAt.IsZero() || creds.CapturedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("CapturedAt = %v", creds.CapturedAt)
	}
}
