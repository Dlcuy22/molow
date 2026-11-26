package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

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
// and notifies onSetCookies with the sign-in generation bound to this client.
type cookieCapturingTransport struct {
	base         http.RoundTripper
	gen          uint64
	onSetCookies func(gen uint64, setCookies []string)
}

func (t *cookieCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if resp != nil && req != nil && req.URL != nil && isYouTubeCookieHost(req.URL.Hostname()) {
		if setCookies := resp.Header.Values("Set-Cookie"); len(setCookies) > 0 && t.onSetCookies != nil {
			t.onSetCookies(t.gen, setCookies)
		}
	}
	return resp, err
}

func (s *PlayerService) clientWithCookieCapture(creds *Credentials, gen uint64) (*ytm.Client, error) {
	return s.clientWithCookieCaptureBase(creds, gen, newYTMHTTPClient().Transport)
}

func (s *PlayerService) clientWithCookieCaptureBase(creds *Credentials, gen uint64, base http.RoundTripper) (*ytm.Client, error) {
	httpClient := &http.Client{
		Transport: &cookieCapturingTransport{
			base:         base,
			gen:          gen,
			onSetCookies: s.applyRotatedCookies,
		},
		Timeout: ytmHeaderTimeout,
	}
	client, err := clientFromCredentialsWithHTTP(creds, httpClient)
	if err != nil {
		return nil, err
	}
	if s.log != nil {
		client.SetLogger(s.log)
	}
	return client, nil
}

// applyRotatedCookies persists Set-Cookie updates into credentials.enc only if
// the caller still belongs to the active sign-in generation.
func (s *PlayerService) applyRotatedCookies(gen uint64, setCookies []string) {
	if len(setCookies) == 0 || gen == 0 || s.authGen.Load() != gen {
		return
	}

	s.cookiePersistMu.Lock()
	defer s.cookiePersistMu.Unlock()

	if s.authGen.Load() != gen {
		return
	}

	creds, err := loadCredentials()
	if err != nil || creds == nil {
		return
	}
	updated, changed := mergeSetCookies(creds.CookieHeader, setCookies)
	if !changed {
		return
	}
	creds.CookieHeader = updated
	if err := saveCredentials(creds); err != nil {
		if s.log != nil {
			s.log.Warn("ytm: persist rotated cookies", "error", err)
		}
		return
	}

	if s.ytmProv != nil {
		s.ytmProv.apiMu.RLock()
		liveClient := s.ytmProv.client
		s.ytmProv.apiMu.RUnlock()
		if liveClient != nil {
			_ = clientFromCredentialsInto(liveClient, creds)
		}
	}
}

// maybeRefreshSession triggers a throttled account probe so Google can issue
// rotated Set-Cookie headers. Repeated calls within 60 s are skipped, and a
// revoked session flips authExpired instead of retrying in a loop.
func (s *PlayerService) maybeRefreshSession(ctx context.Context) error {
	if s.authExpired.Load() {
		return ytm.ErrSessionExpired
	}

	s.cookieRefreshMu.Lock()
	now := time.Now()
	if !s.lastCookieRefresh.IsZero() && now.Sub(s.lastCookieRefresh) < minCookieRefreshInterval {
		s.cookieRefreshMu.Unlock()
		return nil
	}
	s.lastCookieRefresh = now
	s.cookieRefreshMu.Unlock()

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	gen := s.authGen.Load()
	if gen == 0 {
		gen = 1
		s.authGen.CompareAndSwap(0, gen)
	}

	client, err := s.clientWithCookieCapture(creds, gen)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, authProbeTimeout)
	defer cancel()
	probe := s.refreshSessionFn
	if probe == nil {
		probe = func(c context.Context, cl *ytm.Client) error {
			_, err := cl.GetAccountInfo(c)
			return err
		}
	}
	if err := probe(probeCtx, client); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			s.authExpired.Store(true)
		}
		return err
	}
	return nil
}
