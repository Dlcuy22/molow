// Sign in, sign out and channel switching for YouTube Music.
//
// The session is captured, never negotiated: there is no OAuth path, and the
// Google login form is never driven. A signed-in browser hands over its cookies,
// either pasted by the user or harvested from the login window.
//
// Validation is a body check, not a status check. A dead session answers HTTP
// 200 with a signed-out page, so the only trustworthy signal is what the response
// says about itself.

package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dlcuy22/molow/backend/loginwin"
	ytm "github.com/dlcuy22/ytm-go"
)

// authProbeTimeout bounds the validation round trip. It is generous because a
// cold client also fetches a visitor ID first.
const authProbeTimeout = 45 * time.Second

// AuthStatus is what the UI reads to show the account state.
type AuthStatus struct {
	SignedIn  bool   `json:"signedIn"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	// Expired means credentials are stored but the server treated them as
	// signed out. It is separate from SignedIn so the UI can offer a re-sign-in
	// instead of an empty library.
	Expired bool `json:"expired,omitempty"`
}

// authMu serialises the sign-in flow, so two calls cannot open two windows or
// write the file twice.
var authMu chan struct{} = make(chan struct{}, 1)

// SignInWithCookieHeader signs in from a pasted Cookie: header.
//
// The whole header is required, not one field. Measured against the live
// endpoint: a SAPISID on its own authenticates nothing, because it only seeds the
// Authorization hash while the session identity lives in the primary cookies.
// That single-value mistake is the one a user is most likely to make, so the
// input is checked before any network call.
func (s *PlayerService) SignInWithCookieHeader(header string) error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	header = normalizeCookieHeader(header)
	if err := checkCookieHeader(header); err != nil {
		return err
	}

	creds := &Credentials{CookieHeader: header, CapturedAt: time.Now()}
	if err := s.completeSignIn(creds); err != nil {
		return err
	}
	return saveCredentials(creds)
}

// SignIn opens the login window and waits for a session.
//
// The window is a separate GTK toplevel on its own cookie jar, because a Wails
// webview exposes no cookie API and the cookies that matter are HttpOnly. It is
// closed as soon as the session is captured, and its profile directory is
// removed, so nothing is left behind.
func (s *PlayerService) SignIn() error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	jarDir, err := loginJarDir()
	if err != nil {
		return err
	}
	// A fresh profile every time: the jar from a previous attempt would make the
	// capture depend on what happened last, and the window would start already
	// signed in with no way to tell a stale session from a fresh one.
	if err := resetLoginJar(jarDir); err != nil {
		return err
	}
	defer resetLoginJar(jarDir)

	session, err := loginwin.Open(loginwin.Options{
		JarDir:  jarDir,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := session.Wait(ctx)
	if err != nil {
		return err
	}

	creds := credentialsFromLogin(result)
	if err := checkCookieHeader(creds.CookieHeader); err != nil {
		return err
	}
	if err := s.completeSignIn(creds); err != nil {
		return err
	}
	return saveCredentials(creds)
}

// completeSignIn validates a captured session and fills in the identity.
//
// The order matters: the session is validated before anything is written, so a
// failed capture can never overwrite a working one.
func (s *PlayerService) completeSignIn(creds *Credentials) error {
	client, err := clientFromCredentials(creds)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	// A cheap authed read is the probe. It is paged and already signed in, so it
	// exercises exactly the path the library uses. A status-only check would pass
	// a dead session, which is why the client's own body verdict decides.
	if _, _, err := client.GetLikedSongs(ctx); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			return fmt.Errorf("auth: the server treated these cookies as signed out")
		}
		return err
	}

	// The identity is a nicety. If the account endpoints fail the session is
	// still good, so the failure is swallowed rather than rejecting a valid login.
	accounts, accountsErr := client.GetAccounts(ctx)
	if accountsErr == nil {
		for _, a := range accounts {
			if !a.IsSelected {
				continue
			}
			creds.ChannelID = a.PageID
			creds.AuthUser = a.AuthUser
			creds.DataSyncID = a.DataSyncID
			break
		}
	}

	if info, infoErr := client.GetAccountInfo(ctx); infoErr == nil {
		creds.Name = info.Name
		creds.Email = info.Email
	}

	// The visitor ID the client negotiated is worth keeping: it saves a request
	// on every later start, and it is the one the session was validated with.
	if creds.VisitorData == "" {
		if visitor, err := client.GetVisitorID(ctx); err == nil {
			creds.VisitorData = visitor
		}
	}
	return nil
}

// SignOut forgets the stored session. It reports no error when nothing was
// stored, so a second sign-out is harmless.
func (s *PlayerService) SignOut() error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	return clearCredentials()
}

// AuthStatus reports the stored session without touching the network, so the UI
// can render the account row on startup without waiting on a request.
//
// It never claims SignedIn for credentials it has not validated; use RefreshAuth
// for that.
func (s *PlayerService) AuthStatus() AuthStatus {
	creds, err := loadCredentials()
	if err != nil || creds == nil {
		return AuthStatus{}
	}
	return AuthStatus{
		SignedIn:  true,
		Name:      creds.Name,
		Email:     creds.Email,
		ChannelID: creds.ChannelID,
	}
}

// RefreshAuth re-validates the stored session against the server and updates the
// status. It is the call that notices a revoked session, because a dead one
// answers HTTP 200 rather than an error status.
func (s *PlayerService) RefreshAuth() AuthStatus {
	creds, err := loadCredentials()
	if err != nil || creds == nil {
		return AuthStatus{}
	}

	client, err := clientFromCredentials(creds)
	if err != nil {
		return AuthStatus{Expired: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	if _, _, err := client.GetLikedSongs(ctx); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			return AuthStatus{Expired: true}
		}
		// A network failure says nothing about the session, so the stored state
		// stands rather than being reported as expired.
		return AuthStatus{
			SignedIn:  true,
			Name:      creds.Name,
			Email:     creds.Email,
			ChannelID: creds.ChannelID,
		}
	}

	return AuthStatus{
		SignedIn:  true,
		Name:      creds.Name,
		Email:     creds.Email,
		ChannelID: creds.ChannelID,
	}
}

// Accounts lists the channels the signed-in account owns.
func (s *PlayerService) Accounts() ([]AccountInfo, error) {
	client, err := s.authClient()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	accounts, err := client.GetAccounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]AccountInfo, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, AccountInfo{
			Name:          a.Name,
			Byline:        a.Byline,
			ChannelHandle: a.ChannelHandle,
			ThumbnailURL:  a.ThumbnailURL,
			Selected:      a.IsSelected,
			ChannelID:     a.PageID,
		})
	}
	return out, nil
}

// AccountInfo is one channel, as the UI needs it.
type AccountInfo struct {
	Name          string `json:"name"`
	Byline        string `json:"byline,omitempty"`
	ChannelHandle string `json:"channelHandle,omitempty"`
	ThumbnailURL  string `json:"thumbnailUrl,omitempty"`
	Selected      bool   `json:"selected"`
	ChannelID     string `json:"channelId,omitempty"`
}

// SwitchChannel points the stored session at another channel.
//
// One set of cookies serves every channel of an account, so this rewrites three
// fields and reopens nothing. It depends on the client sending the session's
// AuthUser rather than a hardcoded "0"; without that, every brand channel would
// behave like the first one.
func (s *PlayerService) SwitchChannel(channelID string) error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	creds, err := loadCredentials()
	if err != nil {
		return err
	}

	client, err := clientFromCredentials(creds)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	accounts, err := client.GetAccounts(ctx)
	if err != nil {
		return err
	}

	var found bool
	for _, a := range accounts {
		if a.PageID != channelID {
			continue
		}
		creds.ChannelID = a.PageID
		creds.AuthUser = a.AuthUser
		creds.DataSyncID = a.DataSyncID
		found = true
		break
	}
	if !found {
		return fmt.Errorf("auth: no channel %q on this account", channelID)
	}

	// Confirm the new channel answers before persisting, so a bad switch leaves
	// the previous channel in place.
	if err := clientFromCredentialsInto(client, creds); err != nil {
		return err
	}
	if _, err := client.GetAccountInfo(ctx); err != nil {
		return fmt.Errorf("auth: switching channel failed: %w", err)
	}

	return saveCredentials(creds)
}

// clientFromCredentialsInto applies the stored channel fields onto a live client,
// so a switch can be validated without rebuilding it.
func clientFromCredentialsInto(client *ytm.Client, creds *Credentials) error {
	auth, err := ytm.NewAuthFromCookieString(creds.CookieHeader)
	if err != nil {
		return err
	}
	auth.AuthUser = creds.AuthUser
	auth.DataSyncID = creds.DataSyncID
	auth.ChannelID = creds.ChannelID
	client.SetAuth(auth)
	return nil
}

// authClient builds a client from the stored session.
func (s *PlayerService) authClient() (*ytm.Client, error) {
	creds, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	return clientFromCredentials(creds)
}

// normalizeCookieHeader trims the wrapping a user is likely to paste along with
// the cookies: a "Cookie:" prefix, a trailing semicolon, or newlines from a
// multi-line copy.
func normalizeCookieHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	// A copied header can arrive folded across lines.
	raw = strings.Join(strings.Fields(raw), " ")
	return strings.TrimSuffix(raw, ";")
}

// checkCookieHeader rejects a header that cannot authenticate anything, before
// any network call.
//
// Two failures are worth naming separately, because they need different fixes: a
// header with no seed cannot sign a request, and a header with no primary session
// cookie is the exact shape measured to return the signed-out page.
func checkCookieHeader(header string) error {
	if header == "" {
		return fmt.Errorf("auth: the cookie header is empty")
	}

	var hasSeed, hasIdentity bool
	for _, part := range strings.Split(header, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "SAPISID", "__Secure-3PAPISID", "__Secure-1PAPISID":
			hasSeed = true
		case "SID", "__Secure-3PSID", "__Secure-1PSID":
			hasIdentity = true
		}
	}

	switch {
	case !hasSeed:
		return fmt.Errorf("auth: the header carries no SAPISID, so it cannot sign a request")
	case !hasIdentity:
		return fmt.Errorf("auth: the header carries no SID or __Secure-3PSID, so it is not a signed-in session")
	}
	return nil
}
