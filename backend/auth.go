// Per-library authentication, validation, and account lifecycle for remote
// music sources (starting with YouTube Music).
//
// Each remote library owns an independent session and encrypted credential
// record under CredentialStore.
package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlcuy22/molow/backend/loginwin"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

// authProbeTimeout bounds the validation round trip. It is generous because a
// cold client also fetches a visitor ID first.
const authProbeTimeout = 45 * time.Second

// LibraryAuthStatus summarizes authentication and health state for one remote
// library row shown in Settings -> Integration and context menus.
type LibraryAuthStatus struct {
	LibraryID      string       `json:"libraryId"`
	LibraryName    string       `json:"libraryName"`
	Kind           library.Kind `json:"kind"`
	SignedIn       bool         `json:"signedIn"`
	Expired        bool         `json:"expired,omitempty"`
	AccountName    string       `json:"accountName,omitempty"`
	AccountSubtext string       `json:"accountSubtext,omitempty"`
	Error          string       `json:"error,omitempty"`
}

// authMu serializes interactive login windows and credential mutations.
var authMu chan struct{} = make(chan struct{}, 1)

// CheckKeyring probes whether the platform secret store (Credential Manager on
// Windows, Secret Service daemon on Linux, Keychain on macOS) is reachable and
// unlocked before opening a sign-in flow.
func (s *PlayerService) CheckKeyring() error {
	return getKeyStore().Probe()
}

// CreateYTMLibraryWithBrowser opens an isolated login window, validates the
// captured cookies, rejects duplicate Google accounts, stores encrypted
// credentials, and registers the new YouTube Music library.
func (s *PlayerService) CreateYTMLibraryWithBrowser(name, description string) (library.Library, error) {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	if err := s.CheckKeyring(); err != nil {
		return library.Library{}, err
	}

	creds, err := captureYTMCredentialsFromBrowser()
	if err != nil {
		return library.Library{}, err
	}
	return s.createYTMLibraryFromCreds(name, description, creds)
}

// CreateYTMLibraryWithCookie validates a pasted Cookie header, rejects duplicate
// Google accounts, stores encrypted credentials, and creates the new YouTube
// Music library.
func (s *PlayerService) CreateYTMLibraryWithCookie(name, description, cookieHeader string) (library.Library, error) {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	if err := s.CheckKeyring(); err != nil {
		return library.Library{}, err
	}

	header := normalizeCookieHeader(cookieHeader)
	if err := checkCookieHeader(header); err != nil {
		return library.Library{}, err
	}

	creds := &Credentials{CookieHeader: header, CapturedAt: time.Now()}
	return s.createYTMLibraryFromCreds(name, description, creds)
}

// ReSignInLibrary refreshes credentials for an existing YouTube Music library
// using the isolated browser login window.
func (s *PlayerService) ReSignInLibrary(libraryID string) error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	row, err := s.requireRemoteLibraryRow(libraryID, library.KindYTM)
	if err != nil {
		return err
	}
	if err := s.CheckKeyring(); err != nil {
		return err
	}

	creds, err := captureYTMCredentialsFromBrowser()
	if err != nil {
		return err
	}
	return s.applyReSignIn(row, creds)
}

// ReSignInLibraryWithCookie refreshes credentials for an existing YouTube Music
// library from a pasted Cookie header.
func (s *PlayerService) ReSignInLibraryWithCookie(libraryID, cookieHeader string) error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	row, err := s.requireRemoteLibraryRow(libraryID, library.KindYTM)
	if err != nil {
		return err
	}
	if err := s.CheckKeyring(); err != nil {
		return err
	}

	header := normalizeCookieHeader(cookieHeader)
	if err := checkCookieHeader(header); err != nil {
		return err
	}

	creds := &Credentials{CookieHeader: header, CapturedAt: time.Now()}
	return s.applyReSignIn(row, creds)
}

// RefreshLibraryAuth probes the stored session for libraryID against YouTube
// Music, updates its expired state, and returns the updated LibraryAuthStatus.
func (s *PlayerService) RefreshLibraryAuth(libraryID string) (LibraryAuthStatus, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return LibraryAuthStatus{}, fmt.Errorf("library: store is not available")
	}
	row, err := store.LibraryRow(libraryID)
	if err != nil {
		return LibraryAuthStatus{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	if row.Kind == library.KindLocal {
		return LibraryAuthStatus{}, fmt.Errorf("auth: %q is not a remote library", libraryID)
	}

	cs, err := s.credentials()
	if err != nil {
		return s.statusForRow(row, err.Error()), err
	}
	mgr := s.ytmManager()

	if row.CredentialID == "" {
		mgr.markSignedOut(row.ID)
		s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
		return s.statusForRow(row, ""), nil
	}

	var creds Credentials
	if err := cs.Load(row.CredentialID, library.KindYTM, &creds); err != nil {
		if errors.Is(err, ErrNoCredentials) {
			mgr.markSignedOut(row.ID)
			s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
			return s.statusForRow(row, ""), nil
		}
		if sess := mgr.session(row.ID); sess != nil {
			sess.mu.Lock()
			sess.sessionErr = err.Error()
			sess.signedIn = false
			sess.mu.Unlock()
		}
		return s.statusForRow(row, err.Error()), err
	}

	sess := mgr.ensureSession(row.ID, row.CredentialID, decodeYTMConfig(row.Config))
	gen := sess.authGen.Load()
	if gen == 0 {
		gen = sess.authGen.Add(1)
	}
	client, err := mgr.clientWithCookieCapture(row.ID, &creds, gen)
	if err != nil {
		sess.authExpired.Store(true)
		s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
		return s.statusForRow(row, ""), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	probe := mgr.probeCredsFn
	if probe == nil {
		probe = func(c context.Context, cl *ytm.Client) error {
			_, _, err := cl.GetLikedSongs(c)
			return err
		}
	}
	if err := probe(ctx, client); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			sess.authExpired.Store(true)
			s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
			return s.statusForRow(row, ""), nil
		}
		// Transient network error leaves the previous state intact.
		return s.statusForRow(row, ""), nil
	}

	_, _, _ = mgr.installSessionCredentials(row, &creds)
	s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
	return s.statusForRow(row, ""), nil
}

// ConnectedLibraries returns status summaries for every configured remote
// library in creation order.
func (s *PlayerService) ConnectedLibraries() []LibraryAuthStatus {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return []LibraryAuthStatus{}
	}
	rows, err := store.LibraryRows()
	if err != nil {
		return []LibraryAuthStatus{}
	}
	out := make([]LibraryAuthStatus, 0, len(rows))
	for _, row := range rows {
		if row.Kind == library.KindLocal {
			continue
		}
		out = append(out, s.statusForRow(row, ""))
	}
	return out
}

// SignOutLibrary clears stored credentials for libraryID, preserving the cached
// SQLite catalogue in signedOut state unless deleteLibrary is true.
func (s *PlayerService) SignOutLibrary(libraryID string, deleteLibrary bool) error {
	authMu <- struct{}{}
	defer func() { <-authMu }()

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return fmt.Errorf("library: store is not available")
	}

	if deleteLibrary {
		return s.RemoveLibrary(libraryID)
	}

	row, err := store.LibraryRow(libraryID)
	if err != nil {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	if row.Kind == library.KindLocal {
		return fmt.Errorf("auth: %q is not a remote library", libraryID)
	}

	if row.CredentialID != "" {
		cs, err := s.credentials()
		if err != nil {
			return err
		}
		if err := cs.Delete(row.CredentialID); err != nil {
			return err
		}
	}

	s.ytmManager().markSignedOut(libraryID)
	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "ready"})
	return nil
}

func (s *PlayerService) statusForRow(row sqlite.LibraryRow, overrideErr string) LibraryAuthStatus {
	cfg := decodeYTMConfig(row.Config)
	st := LibraryAuthStatus{
		LibraryID:      row.ID,
		LibraryName:    row.Name,
		Kind:           row.Kind,
		AccountName:    cfg.AccountName,
		AccountSubtext: cfg.Email,
	}

	sess := s.ytmManager().session(row.ID)
	if sess != nil {
		sess.mu.RLock()
		st.SignedIn = sess.signedIn
		if sess.config.AccountName != "" {
			st.AccountName = sess.config.AccountName
		}
		if sess.config.Email != "" {
			st.AccountSubtext = sess.config.Email
		}
		errText := sess.sessionErr
		sess.mu.RUnlock()

		st.Expired = sess.authExpired.Load()
		if st.Expired {
			st.SignedIn = false
		}
		if errText != "" {
			st.Error = errText
		}
	} else if row.CredentialID != "" {
		if cs, err := s.credentials(); err == nil && cs.Exists(row.CredentialID) {
			st.SignedIn = true
		}
	}
	if overrideErr != "" {
		st.Error = overrideErr
	}
	return st
}

func (s *PlayerService) requireRemoteLibraryRow(libraryID string, wantKind library.Kind) (sqlite.LibraryRow, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return sqlite.LibraryRow{}, fmt.Errorf("library: store is not available")
	}
	row, err := store.LibraryRow(libraryID)
	if err != nil {
		return sqlite.LibraryRow{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	if row.Kind != wantKind {
		return sqlite.LibraryRow{}, fmt.Errorf("library: %q is not a %s library", libraryID, wantKind)
	}
	return row, nil
}

func (s *PlayerService) createYTMLibraryFromCreds(name, description string, creds *Credentials) (library.Library, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > maxLibraryDescRunes {
		return library.Library{}, fmt.Errorf("library: description must be %d characters or fewer", maxLibraryDescRunes)
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxLibraryNameRunes {
		return library.Library{}, fmt.Errorf("library: name must be %d characters or fewer", maxLibraryNameRunes)
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}
	cs, err := s.credentials()
	if err != nil {
		return library.Library{}, err
	}

	if err := s.validateYTMSession(creds); err != nil {
		return library.Library{}, err
	}

	if dup, found := findDuplicateYTMLibrary(store, cs, creds, ""); found {
		return library.Library{}, fmt.Errorf("auth: this YouTube Music account is already connected to library %q", dup.Name)
	}

	if name == "" {
		name = firstNonEmpty(strings.TrimSpace(creds.Name), strings.TrimSpace(creds.Email), "YouTube Music")
		if utf8.RuneCountInString(name) > maxLibraryNameRunes {
			runes := []rune(name)
			name = string(runes[:maxLibraryNameRunes])
		}
	}

	libID, err := newRemoteLibraryID(library.KindYTM)
	if err != nil {
		return library.Library{}, fmt.Errorf("library: generate id: %w", err)
	}
	credID, err := newCredentialID()
	if err != nil {
		return library.Library{}, fmt.Errorf("auth: generate credential id: %w", err)
	}

	if err := cs.Save(credID, library.KindYTM, creds); err != nil {
		return library.Library{}, err
	}

	row, err := store.AddLibraryRow(sqlite.LibraryRow{
		Library: library.Library{
			ID:          libID,
			Kind:        library.KindYTM,
			Name:        name,
			Description: description,
			Status:      library.StatusReady,
		},
		AccountID:    creds.ChannelID,
		CredentialID: credID,
		Config:       encodeYTMConfig(creds),
	})
	if err != nil {
		_ = cs.Delete(credID)
		return library.Library{}, err
	}

	mgr := s.ytmManager()
	if _, _, err := mgr.installSessionCredentials(row, creds); err != nil {
		_ = store.RemoveLibrary(row.ID)
		_ = cs.Delete(credID)
		return library.Library{}, err
	}
	src := s.registerYTM(row.Library)
	s.triggerYTMSync(row.ID, syncTriggerManual)
	s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})

	if described, err := src.Describe(s.ctx); err == nil {
		return described, nil
	}
	return row.Library, nil
}

func (s *PlayerService) applyReSignIn(row sqlite.LibraryRow, creds *Credentials) error {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return fmt.Errorf("library: store is not available")
	}
	cs, err := s.credentials()
	if err != nil {
		return err
	}

	if err := s.validateYTMSession(creds); err != nil {
		return err
	}

	if dup, found := findDuplicateYTMLibrary(store, cs, creds, row.ID); found {
		return fmt.Errorf("auth: this YouTube Music account is already connected to library %q", dup.Name)
	}
	recorded := decodeYTMConfig(row.Config)
	recordedChannel := recordedYTMChannelID(row)
	if recorded.Email != "" && creds.Email != "" && !strings.EqualFold(recorded.Email, creds.Email) {
		return fmt.Errorf("auth: signed-in account does not match library %q", row.Name)
	}
	if recordedChannel != "" && creds.ChannelID != "" && recordedChannel != creds.ChannelID {
		return fmt.Errorf("auth: signed-in channel does not match library %q", row.Name)
	}

	credID := row.CredentialID
	if credID == "" {
		credID, err = newCredentialID()
		if err != nil {
			return fmt.Errorf("auth: generate credential id: %w", err)
		}
	}
	if err := cs.Save(credID, library.KindYTM, creds); err != nil {
		return err
	}
	if err := store.UpdateLibraryCredential(row.ID, credID); err != nil {
		return err
	}
	if err := store.UpdateLibraryConfig(row.ID, encodeYTMConfig(creds)); err != nil {
		return err
	}
	row.CredentialID = credID
	row.Config = encodeYTMConfig(creds)

	if _, _, err := s.ytmManager().installSessionCredentials(row, creds); err != nil {
		return err
	}
	s.registerYTM(row.Library)
	s.triggerYTMSync(row.ID, syncTriggerManual)
	s.emit(eventLibrary, LibraryProgress{LibraryID: row.ID, State: "ready"})
	return nil
}

func (s *PlayerService) validateYTMSession(creds *Credentials) error {
	if fn := s.ytmManager().validateCredsFn; fn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
		defer cancel()
		return fn(ctx, creds)
	}
	return validateYTMCredentials(creds)
}

func validateYTMCredentials(creds *Credentials) error {
	client, err := clientFromCredentials(creds)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	// A cheap authed read is the probe. It exercises the exact endpoint path
	// used by sync and lets ytm-go classify dead cookies as ErrSessionExpired.
	if _, _, err := client.GetLikedSongs(ctx); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			return fmt.Errorf("auth: the server treated these cookies as signed out")
		}
		return err
	}

	if accounts, accountsErr := client.GetAccounts(ctx); accountsErr == nil {
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

	if creds.VisitorData == "" {
		if visitor, err := client.GetVisitorID(ctx); err == nil {
			creds.VisitorData = visitor
		}
	}
	return nil
}

func findDuplicateYTMLibrary(store *sqlite.Store, cs *CredentialStore, creds *Credentials, excludeLibID string) (sqlite.LibraryRow, bool) {
	if store == nil || creds == nil {
		return sqlite.LibraryRow{}, false
	}
	targetEmail := strings.TrimSpace(creds.Email)
	targetChannel := strings.TrimSpace(creds.ChannelID)
	if targetEmail == "" && targetChannel == "" {
		return sqlite.LibraryRow{}, false
	}

	rows, err := store.LibraryRows()
	if err != nil {
		return sqlite.LibraryRow{}, false
	}
	for _, row := range rows {
		if row.Kind != library.KindYTM || row.ID == excludeLibID {
			continue
		}
		cfg := decodeYTMConfig(row.Config)
		existingEmail := strings.TrimSpace(cfg.Email)
		existingChannel := recordedYTMChannelID(row)
		if (existingEmail == "" || existingChannel == "") && row.CredentialID != "" && cs != nil {
			var stored Credentials
			if err := cs.Load(row.CredentialID, library.KindYTM, &stored); err == nil {
				if existingEmail == "" {
					existingEmail = strings.TrimSpace(stored.Email)
				}
				if existingChannel == "" {
					existingChannel = strings.TrimSpace(stored.ChannelID)
				}
			}
		}
		if targetEmail != "" && existingEmail != "" && strings.EqualFold(targetEmail, existingEmail) {
			return row, true
		}
		if targetChannel != "" && existingChannel != "" && targetChannel == existingChannel {
			return row, true
		}
	}
	return sqlite.LibraryRow{}, false
}

// recordedYTMChannelID returns the YouTube channel ID stored for row, ignoring
// synthetic ytm_<hex> library IDs that earlier store fallbacks copied into account_id.
func recordedYTMChannelID(row sqlite.LibraryRow) string {
	if ch := strings.TrimSpace(decodeYTMConfig(row.Config).ChannelID); ch != "" {
		return ch
	}
	raw := strings.TrimSpace(row.AccountID)
	switch {
	case strings.HasPrefix(raw, "ytm:"):
		raw = strings.TrimPrefix(raw, "ytm:")
	case strings.HasPrefix(raw, "ytm_"):
		rest := strings.TrimPrefix(raw, "ytm_")
		if !strings.HasPrefix(rest, "UC") {
			return ""
		}
		raw = rest
	}
	if raw == "" || raw == "default" {
		return ""
	}
	return raw
}

func captureYTMCredentialsFromBrowser() (*Credentials, error) {
	jarDir, err := loginJarDir()
	if err != nil {
		return nil, err
	}
	if err := resetLoginJar(jarDir); err != nil {
		return nil, err
	}
	defer resetLoginJar(jarDir)

	session, err := loginwin.Open(loginwin.Options{
		JarDir:  jarDir,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := session.Wait(ctx)
	if err != nil {
		return nil, err
	}

	creds := credentialsFromLogin(result)
	if err := checkCookieHeader(creds.CookieHeader); err != nil {
		return nil, err
	}
	return creds, nil
}

// clientFromCredentialsInto applies the stored channel fields onto a live client.
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

// normalizeCookieHeader trims common wrappers users copy alongside cookies.
func normalizeCookieHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	raw = strings.Join(strings.Fields(raw), " ")
	return strings.TrimSuffix(raw, ";")
}

// checkCookieHeader rejects headers missing either the SAPISID seed or primary
// session identity cookie before making network requests.
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
