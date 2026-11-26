// Signed-in YouTube Music credentials: where they live, how they are encrypted
// with AES-256-GCM using a key in the OS keyring, and how they become a client.
//
// They live in credentials.enc (not settings.json) as an authenticated
// ciphertext envelope. The file is 0600 and replaced atomically so a crash
// mid-write cannot leave half a session behind.
package backend

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
	ytm "github.com/dlcuy22/ytm-go"
)

const (
	// credentialsVersion is bumped when the JSON payload shape changes incompatibly.
	credentialsVersion = 1
	// envelopeFormatV1 prefixes credentials.enc so format mismatches fail fast.
	envelopeFormatV1 = byte(1)
	credentialsAAD   = "molow-ytm-credentials-v1"
)

// ErrNoCredentials means no signed-in session is stored.
var ErrNoCredentials = errors.New("auth: not signed in")

// Credentials is one captured session.
//
// CookieHeader is the ytm-go contract, replayed verbatim.
//
// DataSyncID is bookkeeping only, for matching a channel in SwitchChannel. It must
// never be sent as user.onBehalfOfUser: measured live, that answers HTTP 500.
type Credentials struct {
	Version      int       `json:"version"`
	CookieHeader string    `json:"cookie_header"`
	VisitorData  string    `json:"visitor_data,omitempty"`
	AuthUser     string    `json:"auth_user,omitempty"`
	DataSyncID   string    `json:"data_sync_id,omitempty"`
	ChannelID    string    `json:"channel_id,omitempty"`
	Name         string    `json:"name,omitempty"`
	Email        string    `json:"email,omitempty"`
	CapturedAt   time.Time `json:"captured_at"`
}

// credentialsPath is where the encrypted session envelope lives, beside settings.json.
func credentialsPath() (string, error) {
	settings, err := settingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "credentials.enc"), nil
}

// loadCredentials decrypts the stored session using the OS keyring master key.
// A missing file or missing keyring entry is reported as ErrNoCredentials so the
// UI prompts a clean sign-in.
func loadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	return loadCredentialsFrom(path, getKeyStore())
}

func loadCredentialsFrom(path string, ks KeyStore) (*Credentials, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoCredentials
		}
		return nil, err
	}

	key, err := ks.GetKey()
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			return nil, ErrNoCredentials
		}
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(blob) < 1+nonceSize+gcm.Overhead() || blob[0] != envelopeFormatV1 {
		return nil, fmt.Errorf("auth: invalid encrypted credentials envelope")
	}

	nonce := blob[1 : 1+nonceSize]
	ciphertext := blob[1+nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(credentialsAAD))
	if err != nil {
		return nil, fmt.Errorf("auth: credentials could not be decrypted: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return nil, fmt.Errorf("auth: credentials file is unreadable: %w", err)
	}
	if creds.Version != credentialsVersion {
		return nil, fmt.Errorf("auth: credentials version %d is not %d", creds.Version, credentialsVersion)
	}
	if creds.CookieHeader == "" {
		return nil, ErrNoCredentials
	}
	return &creds, nil
}

// saveCredentials encrypts creds with AES-256-GCM and writes credentials.enc
// atomically with 0600 permissions.
func saveCredentials(creds *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	return saveCredentialsTo(path, getKeyStore(), creds)
}

func saveCredentialsTo(path string, ks KeyStore, creds *Credentials) error {
	if creds == nil || creds.CookieHeader == "" {
		return errors.New("auth: refusing to save empty credentials")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	copyCreds := *creds
	copyCreds.Version = credentialsVersion
	if copyCreds.CapturedAt.IsZero() {
		copyCreds.CapturedAt = time.Now()
	}

	plaintext, err := json.Marshal(copyCreds)
	if err != nil {
		return err
	}

	key, err := ks.GetOrCreateKey()
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	sealed := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	sealed = append(sealed, envelopeFormatV1)
	sealed = append(sealed, nonce...)
	sealed = gcm.Seal(sealed, nonce, plaintext, []byte(credentialsAAD))

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}

	// The mode is set before the write so the file never exists with the wrong
	// permissions and encrypted content at the same time.
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(sealed); err != nil {
		cleanup()
		return err
	}
	// Flush before the rename so the rename cannot publish an empty file.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// clearCredentials removes the encrypted session file and its keyring master key.
// Missing entries are ignored so signing out twice is harmless.
func clearCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := getKeyStore().DeleteKey(); err != nil && !errors.Is(err, ErrKeyNotFound) {
		return err
	}
	return nil
}

// clientFromCredentials builds a client from a stored session.
//
// The channel fields are applied to the AuthState, but only AuthUser ever leaves
// the process: it becomes the X-Goog-AuthUser header. ChannelID is read locally
// for playlist ownership, and DataSyncID stays in the encrypted store.
func clientFromCredentials(creds *Credentials) (*ytm.Client, error) {
	return clientFromCredentialsWithHTTP(creds, nil)
}

func clientFromCredentialsWithHTTP(creds *Credentials, httpClient *http.Client) (*ytm.Client, error) {
	if creds == nil || creds.CookieHeader == "" {
		return nil, ErrNoCredentials
	}

	auth, err := ytm.NewAuthFromCookieString(creds.CookieHeader)
	if err != nil {
		return nil, err
	}
	auth.AuthUser = creds.AuthUser
	auth.DataSyncID = creds.DataSyncID
	auth.ChannelID = creds.ChannelID

	var client *ytm.Client
	if httpClient != nil {
		client = ytm.NewClientWithHTTP(httpClient)
	} else {
		client = ytm.NewClient()
	}
	client.SetAuth(auth)
	if creds.VisitorData != "" {
		client.SetVisitorID(creds.VisitorData)
	}
	return client, nil
}

// credentialsFromLogin converts a login window result into a stored session.
func credentialsFromLogin(result loginwin.Result) *Credentials {
	return &Credentials{
		CookieHeader: result.CookieHeader,
		VisitorData:  result.VisitorData,
		AuthUser:     result.AuthUser,
		DataSyncID:   result.DataSyncID,
		CapturedAt:   time.Now(),
	}
}
