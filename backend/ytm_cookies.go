package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dlcuy22/molow/internal/library"
	ytm "github.com/dlcuy22/ytm-go"
)

const minCookieRefreshInterval = 60 * time.Second

func isYouTubeCookieHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "music.youtube.com" || h == "youtube.com" || strings.HasSuffix(h, ".youtube.com")
}

func isAllowedCookieDomain(domain string) bool {
	d := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	switch d {
	case "music.youtube.com", "youtube.com", "google.com":
		return true
	}
	return false
}

// mergeSetCookies folds Set-Cookie updates from YouTube HTTP responses onto
// an existing Cookie header string, preserving order and ignoring empty or
// expired deletions so a transient header cannot wipe active credentials.
func mergeSetCookies(existing string, setCookieHeaders []string) (string, bool) {
	if len(setCookieHeaders) == 0 {
		return existing, false
	}

	var order []string
	vals := make(map[string]string)

	for _, part := range strings.Split(existing, ";") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		if _, exists := vals[name]; !exists {
			order = append(order, name)
		}
		vals[name] = strings.TrimSpace(val)
	}

	hdr := http.Header{"Set-Cookie": setCookieHeaders}
	parsed := (&http.Response{Header: hdr}).Cookies()

	changed := false
	for _, c := range parsed {
		if c.Name == "" {
			continue
		}
		if c.Domain != "" && !isAllowedCookieDomain(c.Domain) {
			continue
		}
		if c.Path != "" && c.Path != "/" {
			continue
		}
		if c.MaxAge < 0 || c.Value == "" {
			continue
		}
		if prev, exists := vals[c.Name]; !exists {
			order = append(order, c.Name)
			vals[c.Name] = c.Value
			changed = true
		} else if prev != c.Value {
			vals[c.Name] = c.Value
			changed = true
		}
	}
	if !changed {
		return existing, false
	}

	var b strings.Builder
	for _, name := range order {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(vals[name])
	}
	return b.String(), true
}

// cookieCapturingTransport inspects YouTube responses for Set-Cookie headers
// and notifies onSetCookies with the library id and sign-in generation bound
// to this client.
type cookieCapturingTransport struct {
	base         http.RoundTripper
	libraryID    string
	gen          uint64
	onSetCookies func(libraryID string, gen uint64, setCookies []string)
}

func (t *cookieCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if resp != nil && req != nil && req.URL != nil && isYouTubeCookieHost(req.URL.Hostname()) {
		if setCookies := resp.Header.Values("Set-Cookie"); len(setCookies) > 0 && t.onSetCookies != nil {
			t.onSetCookies(t.libraryID, t.gen, setCookies)
		}
	}
	return resp, err
}

func (m *ytmManager) clientWithCookieCapture(libraryID string, creds *Credentials, gen uint64) (*ytm.Client, error) {
	return m.clientWithCookieCaptureBase(libraryID, creds, gen, newYTMHTTPClient().Transport)
}

func (m *ytmManager) clientWithCookieCaptureBase(libraryID string, creds *Credentials, gen uint64, base http.RoundTripper) (*ytm.Client, error) {
	httpClient := &http.Client{
		Transport: &cookieCapturingTransport{
			base:         base,
			libraryID:    libraryID,
			gen:          gen,
			onSetCookies: m.applyRotatedCookies,
		},
		Timeout: ytmHeaderTimeout,
	}
	client, err := clientFromCredentialsWithHTTP(creds, httpClient)
	if err != nil {
		return nil, err
	}
	client.SetLogger(m.log())
	return client, nil
}

// applyRotatedCookies persists Set-Cookie updates strictly into libraryID's own
// credential file and only if the caller still belongs to that session's active
// sign-in generation.
func (m *ytmManager) applyRotatedCookies(libraryID string, gen uint64, setCookies []string) {
	if len(setCookies) == 0 || gen == 0 || libraryID == "" || m == nil {
		return
	}
	sess := m.session(libraryID)
	if sess == nil || sess.authGen.Load() != gen {
		return
	}

	sess.cookiePersistMu.Lock()
	defer sess.cookiePersistMu.Unlock()

	if sess.authGen.Load() != gen {
		return
	}

	sess.mu.RLock()
	credID := sess.credentialID
	sess.mu.RUnlock()
	if credID == "" || m.svc == nil {
		return
	}

	cs, err := m.svc.credentials()
	if err != nil {
		return
	}
	var creds Credentials
	if err := cs.Load(credID, library.KindYTM, &creds); err != nil {
		return
	}
	updated, changed := mergeSetCookies(creds.CookieHeader, setCookies)
	if !changed {
		return
	}
	creds.CookieHeader = updated
	if err := cs.Save(credID, library.KindYTM, &creds); err != nil {
		m.log().Warn("ytm: persist rotated cookies", "library", libraryID, "error", err)
		return
	}

	sess.mu.RLock()
	liveClient := sess.client
	sess.mu.RUnlock()
	if liveClient != nil {
		_ = clientFromCredentialsInto(liveClient, &creds)
	}
}

// maybeRefreshSession triggers a throttled account probe for libraryID so
// Google can issue rotated Set-Cookie headers. Repeated calls within 60 s are
// skipped, and a revoked session flips that session's authExpired flag instead
// of retrying in a loop.
func (m *ytmManager) maybeRefreshSession(ctx context.Context, libraryID string) error {
	if m == nil || libraryID == "" {
		return ErrNoCredentials
	}
	sess := m.session(libraryID)
	if sess == nil {
		return ErrNoCredentials
	}
	if sess.authExpired.Load() {
		return ytm.ErrSessionExpired
	}

	sess.cookieRefreshMu.Lock()
	now := time.Now()
	if !sess.lastCookieRefresh.IsZero() && now.Sub(sess.lastCookieRefresh) < minCookieRefreshInterval {
		sess.cookieRefreshMu.Unlock()
		return nil
	}
	sess.lastCookieRefresh = now
	sess.cookieRefreshMu.Unlock()

	sess.mu.RLock()
	credID := sess.credentialID
	sess.mu.RUnlock()
	if credID == "" || m.svc == nil {
		return ErrNoCredentials
	}

	cs, err := m.svc.credentials()
	if err != nil {
		return err
	}
	var creds Credentials
	if err := cs.Load(credID, library.KindYTM, &creds); err != nil {
		return err
	}

	gen := sess.authGen.Load()
	if gen == 0 {
		gen = 1
		sess.authGen.CompareAndSwap(0, gen)
	}

	client, err := m.clientWithCookieCapture(libraryID, &creds, gen)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, authProbeTimeout)
	defer cancel()
	probe := m.refreshSessionFn
	if probe == nil {
		probe = func(c context.Context, cl *ytm.Client) error {
			_, err := cl.GetAccountInfo(c)
			return err
		}
	}
	if err := probe(probeCtx, client); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			sess.authExpired.Store(true)
		}
		return err
	}
	return nil
}
