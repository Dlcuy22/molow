package backend

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dlcuy22/molo/provider"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"github.com/dlcuy22/molow/internal/lyrics"
	ytm "github.com/dlcuy22/ytm-go"
)

// ytmSession holds the runtime state and credential link for one KindYTM
// library instance.
type ytmSession struct {
	libraryID    string
	credentialID string
	config       ytmLibraryConfig

	mu         sync.RWMutex
	client     *ytm.Client
	signedIn   bool
	sessionErr string

	authGen           atomic.Uint64
	authExpired       atomic.Bool
	cookiePersistMu   sync.Mutex
	cookieRefreshMu   sync.Mutex
	lastCookieRefresh time.Time
}

func (s *ytmSession) isSignedIn() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.signedIn
}

// ytmManager manages all YouTube Music library sessions and exposes SourceAdapter,
// CoverFetcher, and LyricFetcher seams onto audioRouter.
type ytmManager struct {
	svc  *PlayerService
	prov *ytmProvider

	mu       sync.RWMutex
	sessions map[string]*ytmSession

	// refreshSessionFn lets unit tests stub active cookie refresh probes.
	refreshSessionFn func(context.Context, *ytm.Client) error
	// validateCredsFn and probeCredsFn let unit tests stub live Google account
	// validation and liveness probes during auth flows.
	validateCredsFn func(context.Context, *Credentials) error
	probeCredsFn    func(context.Context, *ytm.Client) error
}

func newYTMManager(svc *PlayerService, prov *ytmProvider) *ytmManager {
	m := &ytmManager{
		svc:      svc,
		prov:     prov,
		sessions: make(map[string]*ytmSession),
	}
	if prov != nil {
		prov.clientResolver = m.clientForRef
		prov.defaultClient = m.resolveDefaultClient
	}
	return m
}

func (m *ytmManager) Kind() library.Kind { return library.KindYTM }

func (m *ytmManager) Provider() provider.AudioProvider {
	if m.svc != nil && m.svc.ytmProv != nil {
		return m.svc.ytmProv
	}
	return m.prov
}

func (m *ytmManager) activeProvider() *ytmProvider {
	if m.svc != nil && m.svc.ytmProv != nil {
		return m.svc.ytmProv
	}
	return m.prov
}

func (m *ytmManager) log() *slog.Logger {
	if m.svc != nil {
		return m.svc.logger()
	}
	return slog.Default()
}

func (m *ytmManager) session(libraryID string) *ytmSession {
	if m == nil || libraryID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[libraryID]
}

func (m *ytmManager) ensureSession(libraryID, credentialID string, cfg ytmLibraryConfig) *ytmSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = make(map[string]*ytmSession)
	}
	sess := m.sessions[libraryID]
	if sess == nil {
		sess = &ytmSession{libraryID: libraryID}
		m.sessions[libraryID] = sess
	}
	sess.mu.Lock()
	if credentialID != "" {
		sess.credentialID = credentialID
	}
	if cfg != (ytmLibraryConfig{}) {
		sess.config = cfg
	}
	sess.mu.Unlock()
	return sess
}

func (m *ytmManager) primarySession() *ytmSession {
	if m == nil {
		return nil
	}
	if m.svc != nil && m.svc.libStore != nil {
		if rows, err := m.svc.libStore.LibraryRows(); err == nil {
			var fallback *ytmSession
			for _, row := range rows {
				if row.Kind != library.KindYTM {
					continue
				}
				sess := m.session(row.ID)
				if sess == nil {
					continue
				}
				if fallback == nil {
					fallback = sess
				}
				sess.mu.RLock()
				ok := sess.signedIn && !sess.authExpired.Load() && sess.client != nil
				sess.mu.RUnlock()
				if ok {
					return sess
				}
			}
			if fallback != nil {
				return fallback
			}
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	var fallback *ytmSession
	for _, sess := range m.sessions {
		if fallback == nil {
			fallback = sess
		}
		sess.mu.RLock()
		ok := sess.signedIn && !sess.authExpired.Load() && sess.client != nil
		sess.mu.RUnlock()
		if ok {
			return sess
		}
	}
	return fallback
}

// resolveDefaultClient resolves the client used for unscoped YTM operations
// (Search, Artist view, and Local-imported YTM playback/sync) in order:
// active YTM library -> primary signed-in YTM library -> shared anonymous client.
func (m *ytmManager) resolveDefaultClient() *ytm.Client {
	if m == nil {
		return ytmClient(slog.Default())
	}
	if m.svc != nil {
		if activeID := m.svc.activeLibrary(); activeID != "" {
			if sess := m.session(activeID); sess != nil && !sess.authExpired.Load() {
				sess.mu.RLock()
				c := sess.client
				ok := sess.signedIn && c != nil
				sess.mu.RUnlock()
				if ok {
					return c
				}
			}
		}
	}
	if m.svc != nil && m.svc.libStore != nil {
		if rows, err := m.svc.libStore.LibraryRows(); err == nil {
			for _, row := range rows {
				if row.Kind != library.KindYTM {
					continue
				}
				if sess := m.session(row.ID); sess != nil && !sess.authExpired.Load() {
					sess.mu.RLock()
					c := sess.client
					ok := sess.signedIn && c != nil
					sess.mu.RUnlock()
					if ok {
						return c
					}
				}
			}
		}
	}
	m.mu.RLock()
	for _, sess := range m.sessions {
		if sess.authExpired.Load() {
			continue
		}
		sess.mu.RLock()
		c := sess.client
		ok := sess.signedIn && c != nil
		sess.mu.RUnlock()
		if ok {
			m.mu.RUnlock()
			return c
		}
	}
	m.mu.RUnlock()

	if p := m.activeProvider(); p != nil {
		p.apiMu.RLock()
		c := p.client
		p.apiMu.RUnlock()
		if c != nil {
			return c
		}
	}
	return ytmClient(m.log())
}

// sessionClient returns the *ytm.Client belonging exclusively to libraryID.
// A signed-out, expired, or unreadable session returns its explicit error rather
// than falling back to another account's credentials.
func (m *ytmManager) sessionClient(libraryID string) (*ytm.Client, error) {
	sess := m.session(libraryID)
	if sess == nil {
		return nil, ErrNoCredentials
	}
	if sess.authExpired.Load() {
		return nil, ytm.ErrSessionExpired
	}
	sess.mu.RLock()
	client := sess.client
	signedIn := sess.signedIn
	errText := sess.sessionErr
	sess.mu.RUnlock()
	if errText != "" {
		return nil, errors.New(errText)
	}
	if !signedIn || client == nil {
		return nil, ErrNoCredentials
	}
	return client, nil
}

// clientForRef resolves scoped YTM refs to the exact owning library session and
// unscoped YTM refs via active -> primary -> anonymous fallback.
func (m *ytmManager) clientForRef(ref string) (*ytm.Client, error) {
	if libID := ytmRefLibraryID(ref); libID != "" {
		return m.sessionClient(libID)
	}
	return m.resolveDefaultClient(), nil
}

func (m *ytmManager) loadSessionCredentials(row sqlite.LibraryRow) (*Credentials, error) {
	if row.CredentialID == "" {
		return nil, ErrNoCredentials
	}
	if m.svc == nil {
		return nil, ErrNoCredentials
	}
	cs, err := m.svc.credentials()
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := cs.Load(row.CredentialID, library.KindYTM, &creds); err != nil {
		return nil, err
	}
	if creds.CookieHeader == "" {
		return nil, ErrNoCredentials
	}
	return &creds, nil
}

func (m *ytmManager) installSessionCredentials(row sqlite.LibraryRow, creds *Credentials) (*ytmSession, *ytm.Client, error) {
	cfg := decodeYTMConfig(row.Config)
	if creds != nil {
		if creds.Name != "" {
			cfg.AccountName = creds.Name
		}
		if creds.Email != "" {
			cfg.Email = creds.Email
		}
		if creds.ChannelID != "" {
			cfg.ChannelID = creds.ChannelID
		}
	}
	sess := m.ensureSession(row.ID, row.CredentialID, cfg)
	gen := sess.authGen.Add(1)
	sess.authExpired.Store(false)

	if creds == nil {
		sess.mu.Lock()
		sess.client = nil
		sess.signedIn = false
		sess.sessionErr = ""
		sess.mu.Unlock()
		return sess, nil, nil
	}

	client, err := m.clientWithCookieCapture(row.ID, creds, gen)
	if err != nil {
		sess.mu.Lock()
		sess.client = nil
		sess.signedIn = false
		sess.sessionErr = err.Error()
		sess.mu.Unlock()
		return sess, nil, err
	}

	sess.mu.Lock()
	sess.client = client
	sess.signedIn = true
	sess.sessionErr = ""
	sess.credentialID = row.CredentialID
	sess.config = cfg
	sess.mu.Unlock()

	if p := m.activeProvider(); p != nil {
		p.clearSheetsForLibrary(row.ID)
	}
	return sess, client, nil
}

// Open restores any stored credential for row and attaches a ytmSource runtime.
func (m *ytmManager) Open(ctx context.Context, row sqlite.LibraryRow) (library.Source, error) {
	cfg := decodeYTMConfig(row.Config)
	sess := m.ensureSession(row.ID, row.CredentialID, cfg)

	creds, err := m.loadSessionCredentials(row)
	switch {
	case err == nil:
		if _, client, cerr := m.installSessionCredentials(row, creds); cerr != nil {
			m.log().Warn("ytm: could not rebuild session", "library", row.ID, "error", cerr)
		} else {
			m.log().Info("ytm: session restored", "library", row.ID, "account", creds.Name, "channel", creds.ChannelID)
			m.verifySessionAsync(sess, client)
		}
	case errors.Is(err, ErrNoCredentials):
		sess.authGen.Add(1)
		sess.authExpired.Store(false)
		sess.mu.Lock()
		sess.client = nil
		sess.signedIn = false
		sess.sessionErr = ""
		sess.mu.Unlock()
	default:
		// Keyring locked / GCM failure: preserve the encrypted file and surface
		// the state without pretending the user signed out.
		sess.authGen.Add(1)
		sess.authExpired.Store(false)
		sess.mu.Lock()
		sess.client = nil
		sess.signedIn = false
		sess.sessionErr = err.Error()
		sess.mu.Unlock()
		m.log().Warn("ytm: stored session is unreadable", "library", row.ID, "error", err)
	}

	src := m.svc.registerYTM(row.Library)
	if creds != nil && m.svc != nil && m.svc.libStore != nil && !sess.authExpired.Load() {
		at, syncErr := m.svc.libStore.SyncTime(row.ID, "playlists")
		if syncErr != nil || at == 0 || time.Since(time.Unix(at, 0)) > ytmOpenStaleAge {
			m.svc.triggerYTMSync(row.ID, syncTriggerOpen)
		}
	}
	_ = ctx
	return src, nil
}

func (m *ytmManager) verifySessionAsync(sess *ytmSession, client *ytm.Client) {
	if m.svc == nil || m.svc.ctx == nil || client == nil {
		return
	}
	m.svc.wg.Add(1)
	go func() {
		defer m.svc.wg.Done()
		ctx, cancel := context.WithTimeout(m.svc.ctx, authProbeTimeout)
		defer cancel()

		gen := sess.authGen.Load()
		if _, _, err := client.GetLikedSongs(ctx); err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) && sess.authGen.Load() == gen {
				sess.authExpired.Store(true)
				m.log().Warn("ytm: stored session expired", "library", sess.libraryID)
				m.svc.emit(eventLibrary, LibraryProgress{LibraryID: sess.libraryID, State: "ready"})
			} else {
				m.log().Debug("ytm: session check skipped", "library", sess.libraryID, "error", err)
			}
			return
		}
		m.log().Debug("ytm: session check passed", "library", sess.libraryID)
	}()
}

func (m *ytmManager) CloseLibrary(libraryID string) {
	if m == nil || libraryID == "" {
		return
	}
	m.mu.Lock()
	sess := m.sessions[libraryID]
	delete(m.sessions, libraryID)
	m.mu.Unlock()
	if sess != nil {
		sess.authGen.Add(1)
	}
	if m.svc != nil {
		m.svc.libMu.Lock()
		old, _ := m.svc.libs[libraryID].(*ytmSource)
		m.svc.libMu.Unlock()
		if old != nil && old.syncer != nil {
			old.syncer.stop()
		}
	}
	if p := m.activeProvider(); p != nil {
		p.clearSheetsForLibrary(libraryID)
	}
}

func (m *ytmManager) markSignedOut(libraryID string) {
	if m == nil || libraryID == "" {
		return
	}
	if sess := m.session(libraryID); sess != nil {
		sess.authGen.Add(1)
		sess.authExpired.Store(false)
		sess.mu.Lock()
		sess.client = nil
		sess.signedIn = false
		sess.sessionErr = ""
		sess.mu.Unlock()
	}
	if m.svc != nil {
		m.svc.libMu.Lock()
		old, _ := m.svc.libs[libraryID].(*ytmSource)
		m.svc.libMu.Unlock()
		if old != nil && old.syncer != nil {
			old.syncer.stop()
		}
	}
	if p := m.activeProvider(); p != nil {
		p.clearSheetsForLibrary(libraryID)
	}
}

func (m *ytmManager) Close() {
	if m.svc != nil {
		m.svc.stopYTMSyncers()
	}
}

func (m *ytmManager) CoverBytes(_ context.Context, ref string, edge int) ([]byte, string, bool) {
	if !isYTMRef(ref) || m.svc == nil || m.svc.art == nil {
		return nil, "", false
	}
	p := m.activeProvider()
	if p == nil {
		return nil, "", false
	}
	t, ok := m.svc.ytm.lookup(ref)
	if !ok {
		if cat, found := m.svc.catalogYTMTrack(ref); found {
			m.svc.ytm.put(cat)
			t, ok = cat, true
		}
	}
	if !ok {
		return nil, "", false
	}
	edge = artwork.NormalizeThumbEdge(edge)
	if cid := t.coverID(); cid != "" {
		if data, mime, ok := m.svc.art.ThumbBytes(cid, edge); ok {
			return data, mime, true
		}
	}
	data := p.fetchThumb(ref)
	if len(data) == 0 {
		return nil, "", false
	}
	cid, err := m.svc.art.Put(data)
	if err != nil {
		return nil, "", false
	}
	return m.svc.art.ThumbBytes(cid, edge)
}

func (m *ytmManager) FetchLyrics(ctx context.Context, ref string) (*lyrics.Document, error) {
	if !isYTMRef(ref) {
		return nil, nil
	}
	p := m.activeProvider()
	if p == nil {
		return nil, nil
	}
	sheet, ok := p.lyricsFor(ctx, ref)
	if !ok || sheet == nil {
		return nil, nil
	}
	return ytmLyricsDocument(sheet), nil
}

// setClient swaps in the client built for a signed-in session, and drops cached
// lyric sheets because a different session can see different ones.
func (p *ytmProvider) setClient(client *ytm.Client) {
	if p == nil || client == nil {
		return
	}

	p.installClient(client)

	p.sheetMu.Lock()
	p.sheets = make(map[string]*ytm.Lyrics)
	p.sheetAt = nil
	p.sheetMu.Unlock()
}
