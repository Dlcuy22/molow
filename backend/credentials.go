// Signed-in YouTube Music credentials: where they live, how they are written,
// and how they become a client.
//
// They get their own file rather than a corner of settings.json, because that
// file is plain JSON with no protection and these are bearer credentials: the
// cookie header alone is enough to act as the account. The file is 0600 and is
// replaced atomically, so a crash mid-write cannot leave half a session behind.

package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
	ytm "github.com/dlcuy22/ytm-go"
)

// credentialsVersion is bumped when the on-disk shape changes incompatibly. A
// file with a different version is ignored rather than half-read.
const credentialsVersion = 1

// ErrNoCredentials means no signed-in session is stored.
var ErrNoCredentials = errors.New("auth: not signed in")

// Credentials is one captured session.
//
// CookieHeader is the ytm-go contract and is replayed verbatim. Cookies carries
// the same set with domain and path intact, which the header throws away and
// Restore needs.
//
// DataSyncID is bookkeeping only. It identifies the active channel for
// SwitchChannel and must never be sent as user.onBehalfOfUser: measured against
// the live endpoint, that answers HTTP 500 INTERNAL for a plain channel-scoped
// session.
type Credentials struct {
	Version      int       `json:"version"`
	CookieHeader string    `json:"cookie_header"`
	Cookies      []Cookie  `json:"cookies,omitempty"`
	VisitorData  string    `json:"visitor_data,omitempty"`
	AuthUser     string    `json:"auth_user,omitempty"`
	DataSyncID   string    `json:"data_sync_id,omitempty"`
	ChannelID    string    `json:"channel_id,omitempty"`
	Name         string    `json:"name,omitempty"`
	Email        string    `json:"email,omitempty"`
	CapturedAt   time.Time `json:"captured_at"`
}

// Cookie mirrors loginwin.Cookie so the stored document does not depend on the
// login window's package staying stable.
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	SameSite string    `json:"same_site,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
}

// credentialsPath is where the session file lives, beside settings.json.
func credentialsPath() (string, error) {
	settings, err := settingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "credentials.json"), nil
}

// loadCredentials reads the stored session. A missing file is reported as
// ErrNoCredentials rather than an error, because "not signed in" is the normal
// state of a fresh install.
func loadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoCredentials
		}
		return nil, err
	}

	var creds Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
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

// saveCredentials writes the session atomically with 0600 permissions.
//
// The temporary file is created in the same directory so the rename stays on one
// filesystem, and it is chmod'd before any content is written, so the secrets
// are never readable by another user even for an instant.
func saveCredentials(creds *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	creds.Version = credentialsVersion
	if creds.CapturedAt.IsZero() {
		creds.CapturedAt = time.Now()
	}

	raw, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}

	// The mode is set before the write, not after, so the file never exists with
	// the wrong permissions and content at the same time.
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return err
	}
	// Flush before the rename, so the rename cannot publish a file whose contents
	// are still only in the page cache.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// clearCredentials removes the stored session. A missing file is not an error,
// so signing out twice is harmless.
func clearCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// clientFromCredentials builds a client from a stored session.
//
// The channel fields are applied to the AuthState, but only AuthUser ever leaves
// the process: it becomes the X-Goog-AuthUser header. ChannelID is read locally
// for playlist ownership, and DataSyncID stays on disk.
func clientFromCredentials(creds *Credentials) (*ytm.Client, error) {
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

	client := ytm.NewClient()
	client.SetAuth(auth)
	if creds.VisitorData != "" {
		client.SetVisitorID(creds.VisitorData)
	}
	return client, nil
}

// credentialsFromLogin converts a login window result into a stored session.
func credentialsFromLogin(result loginwin.Result) *Credentials {
	creds := &Credentials{
		CookieHeader: result.CookieHeader,
		VisitorData:  result.VisitorData,
		AuthUser:     result.AuthUser,
		DataSyncID:   result.DataSyncID,
		CapturedAt:   time.Now(),
	}
	for _, c := range result.Cookies {
		creds.Cookies = append(creds.Cookies, Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			Expires:  c.Expires,
		})
	}
	return creds
}

// loginCookies converts the stored set back into the login window's form, for
// Restore.
func (c *Credentials) loginCookies() []loginwin.Cookie {
	out := make([]loginwin.Cookie, 0, len(c.Cookies))
	for _, ck := range c.Cookies {
		out = append(out, loginwin.Cookie{
			Name:     ck.Name,
			Value:    ck.Value,
			Domain:   ck.Domain,
			Path:     ck.Path,
			Secure:   ck.Secure,
			HTTPOnly: ck.HTTPOnly,
			SameSite: ck.SameSite,
			Expires:  ck.Expires,
		})
	}
	return out
}
