// Encrypted credential storage backed by the OS keyring master key.
//
// Each remote library owns one encrypted envelope file under
// <configDir>/credentials/<credentialId>.enc, encrypted with AES-256-GCM and
// bound by per-record AAD ("molow-cred-v1:" + kind + ":" + id) so ciphertext
// cannot be swapped across source kinds or library credentials.
package backend

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

const (
	// credentialsVersion is bumped when the JSON payload shape changes incompatibly.
	credentialsVersion = 1
	// envelopeFormatV1 prefixes encrypted credential files so format mismatches fail fast.
	envelopeFormatV1 = byte(1)
	credentialsAAD   = "molow-ytm-credentials-v1"
	credRecordAAD    = "molow-cred-v1:"
)

// ErrNoCredentials means no signed-in session is stored.
var ErrNoCredentials = errors.New("auth: not signed in")

// CredentialRecord is the decrypted JSON envelope stored in
// credentials/<credentialId>.enc.
type CredentialRecord struct {
	Version    int             `json:"version"`
	ID         string          `json:"id"`
	Kind       library.Kind    `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	CapturedAt time.Time       `json:"captured_at"`
}

// ytmLibraryConfig holds non-secret YouTube Music account metadata persisted in
// library.config so Settings and duplicate-account checks work offline or
// while signed out without decrypting credentials.
type ytmLibraryConfig struct {
	AccountName string `json:"accountName,omitempty"`
	Email       string `json:"email,omitempty"`
	ChannelID   string `json:"channelId,omitempty"`
}

func encodeYTMConfig(creds *Credentials) string {
	if creds == nil {
		return ""
	}
	cfg := ytmLibraryConfig{
		AccountName: strings.TrimSpace(creds.Name),
		Email:       strings.TrimSpace(creds.Email),
		ChannelID:   strings.TrimSpace(creds.ChannelID),
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeYTMConfig(raw string) ytmLibraryConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ytmLibraryConfig{}
	}
	var cfg ytmLibraryConfig
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

// CredentialStore stores per-library encrypted credential envelopes under dir,
// sharing a single AES-256 master key in ks.
type CredentialStore struct {
	dir string
	ks  KeyStore
}

func newCredentialStore(dir string, ks KeyStore) *CredentialStore {
	if ks == nil {
		ks = getKeyStore()
	}
	return &CredentialStore{dir: dir, ks: ks}
}

func defaultCredentialStore() (*CredentialStore, error) {
	settings, err := settingsPath()
	if err != nil {
		return nil, err
	}
	return newCredentialStore(filepath.Join(filepath.Dir(settings), "credentials"), getKeyStore()), nil
}

// newCredentialID returns a random filesystem-safe opaque credential identifier.
func newCredentialID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "cred_" + hex.EncodeToString(raw[:]), nil
}

func newRemoteLibraryID(kind library.Kind) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return string(kind) + "_" + hex.EncodeToString(raw[:]), nil
}

func legacyNormalizedLibraryID(kind library.Kind, oldID string) string {
	sum := sha256.Sum256([]byte(oldID))
	return string(kind) + "_" + hex.EncodeToString(sum[:8])
}

func validCredentialID(id string) bool {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\:`) {
		return false
	}
	return true
}

func recordAAD(kind library.Kind, id string) []byte {
	return []byte(credRecordAAD + string(kind) + ":" + id)
}

func (c *CredentialStore) path(id string) (string, error) {
	if !validCredentialID(id) {
		return "", fmt.Errorf("auth: invalid credential id %q", id)
	}
	return filepath.Join(c.dir, id+".enc"), nil
}

// Exists reports whether an encrypted credential file exists for id.
func (c *CredentialStore) Exists(id string) bool {
	p, err := c.path(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Save encrypts payload inside a CredentialRecord and writes it atomically to
// credentials/<id>.enc with 0600 permissions.
func (c *CredentialStore) Save(id string, kind library.Kind, payload any) error {
	if c == nil {
		return errors.New("auth: credential store is nil")
	}
	if kind == "" {
		return errors.New("auth: credential kind is required")
	}
	targetPath, err := c.path(id)
	if err != nil {
		return err
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(rawPayload) == 0 || string(rawPayload) == "null" {
		return errors.New("auth: refusing to save empty credential payload")
	}
	rec := CredentialRecord{
		Version:    credentialsVersion,
		ID:         id,
		Kind:       kind,
		Payload:    rawPayload,
		CapturedAt: time.Now(),
	}
	plaintext, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}

	key, err := c.ks.GetOrCreateKey()
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
	sealed = gcm.Seal(sealed, nonce, plaintext, recordAAD(kind, id))

	return writeSecretFileAtomic(c.dir, targetPath, sealed)
}

// Load decrypts credentials/<id>.enc and unmarshals its payload into dst.
//
// Missing files or ErrKeyNotFound return ErrNoCredentials. When the OS keyring
// is locked/unavailable or GCM decryption fails, Load returns an explicit error
// so callers never confuse a secret-store outage with being signed out.
func (c *CredentialStore) Load(id string, kind library.Kind, dst any) error {
	if c == nil {
		return errors.New("auth: credential store is nil")
	}
	targetPath, err := c.path(id)
	if err != nil {
		return err
	}
	blob, err := os.ReadFile(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoCredentials
		}
		return err
	}

	key, err := c.ks.GetKey()
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			return ErrNoCredentials
		}
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
	nonceSize := gcm.NonceSize()
	if len(blob) < 1+nonceSize+gcm.Overhead() || blob[0] != envelopeFormatV1 {
		return fmt.Errorf("auth: invalid encrypted credential envelope")
	}

	nonce := blob[1 : 1+nonceSize]
	ciphertext := blob[1+nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, recordAAD(kind, id))
	if err != nil {
		return fmt.Errorf("auth: credentials could not be decrypted: %w", err)
	}

	var rec CredentialRecord
	if err := json.Unmarshal(plaintext, &rec); err != nil {
		return fmt.Errorf("auth: credential record is unreadable: %w", err)
	}
	if rec.Version != credentialsVersion {
		return fmt.Errorf("auth: credential record version %d is not %d", rec.Version, credentialsVersion)
	}
	if rec.ID != id || rec.Kind != kind {
		return fmt.Errorf("auth: credential binding mismatch (%s/%s)", rec.Kind, rec.ID)
	}
	if dst != nil {
		if err := json.Unmarshal(rec.Payload, dst); err != nil {
			return fmt.Errorf("auth: credential payload is unreadable: %w", err)
		}
	}
	return nil
}

// Delete removes credentials/<id>.enc while keeping the OS keyring master key
// intact so other signed-in libraries retain decryption access. It refuses
// deletion if the keyring is unavailable or locked.
func (c *CredentialStore) Delete(id string) error {
	if c == nil || id == "" {
		return nil
	}
	targetPath, err := c.path(id)
	if err != nil {
		return err
	}
	if err := c.ks.Probe(); err != nil {
		return err
	}
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeSecretFileAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".cred-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
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

// credentialsPath is where the legacy single-account session envelope lived,
// beside settings.json.
func credentialsPath() (string, error) {
	settings, err := settingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "credentials.enc"), nil
}

func loadLegacyCredentialsFrom(path string, ks KeyStore) (*Credentials, error) {
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

// migrateLegacyCredentials normalizes any legacy colon-containing YTM library ID
// and moves a pre-v9 single-account credentials.enc file into per-library
// storage under CredentialStore.
//
// If decrypting the legacy file fails (including when the OS keyring is locked),
// migration aborts cleanly and leaves credentials.enc untouched.
func migrateLegacyCredentials(store *sqlite.Store, credStore *CredentialStore, legacyPath string, log *slog.Logger) error {
	if store == nil {
		return nil
	}

	rows, err := store.LibraryRows()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Kind == library.KindYTM && strings.Contains(row.ID, ":") {
			newID := legacyNormalizedLibraryID(library.KindYTM, row.ID)
			if err := store.NormalizeLegacyLibraryID(row.ID, newID); err != nil {
				return fmt.Errorf("auth: normalize legacy ytm library %q: %w", row.ID, err)
			}
			if err := store.MigrateUnscopedRemoteLikes(newID); err != nil {
				return fmt.Errorf("auth: migrate legacy ytm likes %q: %w", newID, err)
			}
			if log != nil {
				log.Info("library: normalized legacy ytm id", "oldId", row.ID, "newId", newID)
			}
		}
	}

	if legacyPath == "" || credStore == nil {
		return nil
	}
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	creds, err := loadLegacyCredentialsFrom(legacyPath, credStore.ks)
	if err != nil {
		if errors.Is(err, ErrNoCredentials) {
			return nil
		}
		return err
	}

	rows, err = store.LibraryRows()
	if err != nil {
		return err
	}

	var target *sqlite.LibraryRow
	for i := range rows {
		if rows[i].Kind != library.KindYTM {
			continue
		}
		cfg := decodeYTMConfig(rows[i].Config)
		if creds.ChannelID != "" && (rows[i].AccountID == creds.ChannelID || cfg.ChannelID == creds.ChannelID) {
			target = &rows[i]
			break
		}
		if target == nil {
			target = &rows[i]
		}
	}

	var libID string
	var credID string
	if target == nil {
		id, err := newRemoteLibraryID(library.KindYTM)
		if err != nil {
			return err
		}
		cid, err := newCredentialID()
		if err != nil {
			return err
		}
		name := firstNonEmpty(strings.TrimSpace(creds.Name), strings.TrimSpace(creds.Email), "YouTube Music")
		created, err := store.AddLibraryRow(sqlite.LibraryRow{
			Library: library.Library{
				ID:          id,
				Kind:        library.KindYTM,
				Name:        name,
				Description: "YouTube Music account",
				Status:      library.StatusReady,
			},
			AccountID:    creds.ChannelID,
			CredentialID: cid,
			Config:       encodeYTMConfig(creds),
		})
		if err != nil {
			return err
		}
		libID = created.ID
		credID = created.CredentialID
	} else {
		libID = target.ID
		credID = target.CredentialID
		if credID == "" {
			credID, err = newCredentialID()
			if err != nil {
				return err
			}
		}
	}

	if creds.Version == 0 {
		creds.Version = credentialsVersion
	}
	if err := credStore.Save(credID, library.KindYTM, creds); err != nil {
		return err
	}

	var check Credentials
	if err := credStore.Load(credID, library.KindYTM, &check); err != nil {
		return fmt.Errorf("auth: verify migrated credential %q: %w", credID, err)
	}
	if check.CookieHeader == "" {
		return fmt.Errorf("auth: migrated credential %q verified empty", credID)
	}

	if err := store.UpdateLibraryCredential(libID, credID); err != nil {
		return err
	}
	if err := store.UpdateLibraryConfig(libID, encodeYTMConfig(creds)); err != nil {
		return err
	}
	if err := store.MigrateUnscopedRemoteLikes(libID); err != nil {
		return err
	}

	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if log != nil {
		log.Info("auth: migrated legacy credentials", "library", libID, "credentialId", credID)
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
