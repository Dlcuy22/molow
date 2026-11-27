package backend

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
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

func saveLegacyCredentialsForTest(t *testing.T, path string, ks KeyStore, creds *Credentials) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := *creds
	rec.Version = credentialsVersion
	if rec.CapturedAt.IsZero() {
		rec.CapturedAt = time.Now()
	}
	plaintext, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ks.GetOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(credentialsAAD))
	out := make([]byte, 0, 1+len(nonce)+len(ciphertext))
	out = append(out, envelopeFormatV1)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	if err := writeSecretFileAtomic(filepath.Dir(path), path, out); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialStoreSaveIsConfidentialAnd0600(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	creds := &Credentials{
		CookieHeader: "SAPISID=secret-seed-value; SID=secret-sid-value",
		Name:         "Test User",
		Email:        "test@example.invalid",
	}
	if err := cs.Save("cred-1", library.KindYTM, creds); err != nil {
		t.Fatalf("cs.Save: %v", err)
	}

	path, err := cs.path("cred-1")
	if err != nil {
		t.Fatalf("cs.path: %v", err)
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
			t.Errorf("credential file contains plaintext %q", needle)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cred-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestCredentialStoreLoadRoundTrip(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	want := &Credentials{
		CookieHeader: "SAPISID=a; SID=b; LOGIN_INFO=c",
		VisitorData:  "visitor",
		AuthUser:     "3",
		DataSyncID:   "123||",
		ChannelID:    "UCchannel",
		Name:         "Test User",
		Email:        "test@example.invalid",
		CapturedAt:   time.Now(),
	}
	if err := cs.Save("cred-rt", library.KindYTM, want); err != nil {
		t.Fatalf("cs.Save: %v", err)
	}

	var got Credentials
	if err := cs.Load("cred-rt", library.KindYTM, &got); err != nil {
		t.Fatalf("cs.Load: %v", err)
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

func TestCredentialStoreMissingAndKeyErrors(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	var got Credentials
	if err := cs.Load("cred-missing", library.KindYTM, &got); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}

	if err := cs.Save("cred-key", library.KindYTM, &Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("cs.Save: %v", err)
	}

	// Missing key in keyring -> ErrNoCredentials (no plaintext fallback).
	if err := getKeyStore().DeleteKey(); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if err := cs.Load("cred-key", library.KindYTM, &got); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("cs.Load with missing key = %v, want ErrNoCredentials", err)
	}
}

func TestCredentialStoreRejectsTamperedOrWrongEnvelope(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	if err := cs.Save("cred-tamper", library.KindYTM, &Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("cs.Save: %v", err)
	}
	path, err := cs.path("cred-tamper")
	if err != nil {
		t.Fatalf("cs.path: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	var got Credentials
	if err := cs.Load("cred-tamper", library.KindYTM, &got); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"cookie_header":"SAPISID=a; SID=b"}`), 0o600); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	if err := cs.Load("cred-tamper", library.KindYTM, &got); err == nil {
		t.Fatal("unencrypted plaintext file was accepted")
	}
}

func TestCredentialStoreDelete(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	if err := cs.Delete("cred-del"); err != nil {
		t.Fatalf("deleting a missing credential should not fail: %v", err)
	}

	if err := cs.Save("cred-del", library.KindYTM, &Credentials{CookieHeader: "SAPISID=a; SID=b"}); err != nil {
		t.Fatalf("cs.Save: %v", err)
	}
	if err := cs.Delete("cred-del"); err != nil {
		t.Fatalf("cs.Delete: %v", err)
	}
	var got Credentials
	if err := cs.Load("cred-del", library.KindYTM, &got); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("error = %v, want ErrNoCredentials after Delete", err)
	}
}

func TestCreateYTMLibraryAbortsWhenKeyringUnavailable(t *testing.T) {
	isolateConfig(t)
	ks := &memKeyStore{probeErr: errors.New("org.freedesktop.secrets unavailable")}
	setKeyStoreForTest(t, ks)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	svc := &PlayerService{credStore: cs}
	if err := svc.CheckKeyring(); err == nil {
		t.Fatal("CheckKeyring succeeded when keyring is unavailable")
	}
	if _, err := svc.CreateYTMLibraryWithBrowser("", ""); err == nil || !strings.Contains(err.Error(), "org.freedesktop.secrets") {
		t.Fatalf("CreateYTMLibraryWithBrowser err = %v, want keyring failure", err)
	}
	if _, err := svc.CreateYTMLibraryWithCookie("", "", "SAPISID=abc; SID=def"); err == nil || !strings.Contains(err.Error(), "org.freedesktop.secrets") {
		t.Fatalf("CreateYTMLibraryWithCookie err = %v, want keyring failure", err)
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

func TestCredentialStoreRoundTripAADAndKeyringFailsafe(t *testing.T) {
	isolateConfig(t)
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatalf("defaultCredentialStore: %v", err)
	}

	credsA := &Credentials{CookieHeader: "SAPISID=a; SID=1", Name: "Account A", ChannelID: "UCa"}
	credsB := &Credentials{CookieHeader: "SAPISID=b; SID=2", Name: "Account B", ChannelID: "UCb"}
	if err := cs.Save("cred_a", library.KindYTM, credsA); err != nil {
		t.Fatalf("Save cred_a: %v", err)
	}
	if err := cs.Save("cred_b", library.KindYTM, credsB); err != nil {
		t.Fatalf("Save cred_b: %v", err)
	}

	var gotA, gotB Credentials
	if err := cs.Load("cred_a", library.KindYTM, &gotA); err != nil || gotA.Name != "Account A" {
		t.Fatalf("Load cred_a = (%+v, %v)", gotA, err)
	}
	if err := cs.Load("cred_b", library.KindYTM, &gotB); err != nil || gotB.Name != "Account B" {
		t.Fatalf("Load cred_b = (%+v, %v)", gotB, err)
	}

	// Wrong kind or swapped file fails AAD verification cleanly.
	if err := cs.Load("cred_a", library.Kind("subsonic"), &gotA); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Load with wrong kind = %v, want explicit AAD error", err)
	}
	pathA, err := cs.path("cred_a")
	if err != nil {
		t.Fatal(err)
	}
	pathB, err := cs.path("cred_b")
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathA, rawB, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cs.Load("cred_a", library.KindYTM, &gotA); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Load swapped ciphertext = %v, want explicit AAD error", err)
	}

	// Deleting cred_a leaves OS keyring master key intact so cred_b still decrypts.
	if err := cs.Delete("cred_a"); err != nil {
		t.Fatalf("Delete cred_a: %v", err)
	}
	if err := cs.Load("cred_a", library.KindYTM, &gotA); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Load deleted cred_a = %v, want ErrNoCredentials", err)
	}
	if err := cs.Load("cred_b", library.KindYTM, &gotB); err != nil || gotB.Name != "Account B" {
		t.Fatalf("cred_b broken after deleting cred_a: (%+v, %v)", gotB, err)
	}

	// Keyring locked / unavailable fails explicitly (not ErrNoCredentials) and
	// blocks destructive deletion.
	ks := getKeyStore().(*memKeyStore)
	ks.mu.Lock()
	ks.probeErr = ErrKeyringUnavailable
	ks.mu.Unlock()
	origKS := cs.ks
	cs.ks = &lockedGetKeyStore{memKeyStore: ks}

	if err := cs.Load("cred_b", library.KindYTM, &gotB); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Load with locked keyring = %v, want explicit error (not ErrNoCredentials)", err)
	}
	if err := cs.Delete("cred_b"); !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("Delete with locked keyring = %v, want ErrKeyringUnavailable", err)
	}
	if !cs.Exists("cred_b") {
		t.Fatal("cred_b was deleted despite locked keyring")
	}
	_ = origKS
}

type lockedGetKeyStore struct {
	*memKeyStore
}

func (l *lockedGetKeyStore) GetKey() ([]byte, error) {
	return nil, ErrKeyringUnavailable
}

func TestMigrateLegacyCredentialsHappyAndLocked(t *testing.T) {
	isolateConfig(t)
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.AddLibrary(library.Library{
		ID: "ytm:UClegacy", Kind: library.KindYTM, Name: "Legacy YTM",
	}); err != nil {
		t.Fatal(err)
	}
	legacyPath, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	creds := &Credentials{
		CookieHeader: "SAPISID=leg; SID=id",
		ChannelID:    "UClegacy",
		Name:         "Legacy User",
		Email:        "legacy@example.invalid",
	}
	saveLegacyCredentialsForTest(t, legacyPath, getKeyStore(), creds)

	// Simulate locked keyring -> migration aborts and keeps legacy credentials.enc.
	lockedCS, err := defaultCredentialStore()
	if err != nil {
		t.Fatal(err)
	}
	lockedCS.ks = &lockedGetKeyStore{memKeyStore: getKeyStore().(*memKeyStore)}
	if err := migrateLegacyCredentials(store, lockedCS, legacyPath, nil); err == nil {
		t.Fatal("expected migration to fail when keyring is locked")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy credentials.enc missing after aborted migration: %v", err)
	}

	// Unlocked keyring -> normalizes ID, migrates into CredentialStore, removes legacy file.
	cs, err := defaultCredentialStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyCredentials(store, cs, legacyPath, nil); err != nil {
		t.Fatalf("migrateLegacyCredentials: %v", err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy credentials.enc still present: %v", err)
	}

	rows, err := store.LibraryRows()
	if err != nil || len(rows) != 1 {
		t.Fatalf("LibraryRows = (%+v, %v)", rows, err)
	}
	if strings.Contains(rows[0].ID, ":") || rows[0].CredentialID == "" {
		t.Fatalf("migrated library row = %+v", rows[0])
	}
	var got Credentials
	if err := cs.Load(rows[0].CredentialID, library.KindYTM, &got); err != nil {
		t.Fatalf("Load migrated credential: %v", err)
	}
	if got.CookieHeader != "SAPISID=leg; SID=id" || got.Email != "legacy@example.invalid" {
		t.Fatalf("migrated creds = %+v", got)
	}
}
