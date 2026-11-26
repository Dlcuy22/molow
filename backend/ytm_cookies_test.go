package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	ytm "github.com/dlcuy22/ytm-go"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMergeSetCookies(t *testing.T) {
	existing := "SAPISID=seed1; SID=sid1; __Secure-1PSIDTS=old-ts; PREF=tz=Asia.Jakarta"

	// Rotated token + new cookie + invalid/deletion cookies that must be ignored.
	updated, changed := mergeSetCookies(existing, []string{
		"__Secure-1PSIDTS=new-ts; Path=/; Domain=.youtube.com; HttpOnly; Secure",
		"SIDCC=fresh-sidcc; Path=/; Domain=.youtube.com; Secure",
		"SAPISID=; Max-Age=0; Path=/; Domain=.youtube.com",
		"OTHER=skip; Path=/sub; Domain=.youtube.com",
		"EVIL=skip; Path=/; Domain=.example.com",
	})
	if !changed {
		t.Fatal("mergeSetCookies changed = false, want true")
	}
	if !strings.Contains(updated, "__Secure-1PSIDTS=new-ts") {
		t.Errorf("updated missing rotated token: %q", updated)
	}
	if !strings.Contains(updated, "SIDCC=fresh-sidcc") {
		t.Errorf("updated missing new cookie: %q", updated)
	}
	if !strings.Contains(updated, "SAPISID=seed1") {
		t.Errorf("updated dropped SAPISID: %q", updated)
	}
	if strings.Contains(updated, "OTHER=skip") || strings.Contains(updated, "EVIL=skip") {
		t.Errorf("updated leaked filtered cookie: %q", updated)
	}

	// Identical values report changed = false so disk is not rewritten needlessly.
	again, changed := mergeSetCookies(updated, []string{
		"SIDCC=fresh-sidcc; Path=/; Domain=.youtube.com",
	})
	if changed || again != updated {
		t.Errorf("no-op merge returned changed=%v updated=%q", changed, again)
	}
}

func TestCookieCapturingTransportPersistsRotatedCookies(t *testing.T) {
	isolateConfig(t)

	creds := &Credentials{
		CookieHeader: "SAPISID=seed1; SID=sid1; SIDCC=old-sidcc",
		Name:         "Test",
	}
	if err := saveCredentials(creds); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	svc := &PlayerService{ytmProv: newYTMProvider(newYTMIndex(), nil, false, nil)}
	gen := svc.authGen.Add(1)

	rt := &cookieCapturingTransport{
		gen: gen,
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Set-Cookie": {"SIDCC=rotated-sidcc; Path=/; Domain=.youtube.com"},
				},
				Body: io.NopCloser(bytes.NewBufferString("{}")),
			}, nil
		}),
		onSetCookies: svc.applyRotatedCookies,
	}

	req, err := http.NewRequest(http.MethodPost, "https://music.youtube.com/youtubei/v1/browse", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	got, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if !strings.Contains(got.CookieHeader, "SIDCC=rotated-sidcc") {
		t.Fatalf("persisted CookieHeader = %q, want rotated SIDCC", got.CookieHeader)
	}
}

func TestApplyRotatedCookiesGenerationGuardAfterSignOut(t *testing.T) {
	isolateConfig(t)

	if err := saveCredentials(&Credentials{CookieHeader: "SAPISID=seed1; SID=sid1"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	svc := &PlayerService{ytmProv: newYTMProvider(newYTMIndex(), nil, false, nil)}
	staleGen := svc.authGen.Add(1)

	if err := svc.SignOut(); err != nil {
		t.Fatalf("SignOut: %v", err)
	}

	// A late Set-Cookie callback carrying the pre-sign-out generation must be ignored
	// and must not recreate credentials.enc.
	svc.applyRotatedCookies(staleGen, []string{"SIDCC=zombie; Path=/; Domain=.youtube.com"})

	if _, err := loadCredentials(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("loadCredentials after stale callback = %v, want ErrNoCredentials", err)
	}
}

func TestMaybeRefreshSessionThrottleAndExpiry(t *testing.T) {
	isolateConfig(t)

	if err := saveCredentials(&Credentials{CookieHeader: "SAPISID=seed1; SID=sid1"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	svc := &PlayerService{ytmProv: newYTMProvider(newYTMIndex(), nil, false, nil)}
	svc.authGen.Store(1)

	calls := 0
	returnExpired := false
	svc.refreshSessionFn = func(ctx context.Context, client *ytm.Client) error {
		calls++
		if returnExpired {
			return ytm.ErrSessionExpired
		}
		return nil
	}

	if err := svc.maybeRefreshSession(context.Background()); err != nil {
		t.Fatalf("first maybeRefreshSession: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// Second call within 60 s must be throttled.
	if err := svc.maybeRefreshSession(context.Background()); err != nil {
		t.Fatalf("throttled maybeRefreshSession: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after throttle = %d, want 1", calls)
	}

	// Expire cooldown, simulate revoked session -> sets authExpired = true and stops retrying.
	svc.cookieRefreshMu.Lock()
	svc.lastCookieRefresh = time.Now().Add(-2 * time.Minute)
	svc.cookieRefreshMu.Unlock()
	returnExpired = true

	err := svc.maybeRefreshSession(context.Background())
	if !errors.Is(err, ytm.ErrSessionExpired) {
		t.Fatalf("expired maybeRefreshSession = %v, want ErrSessionExpired", err)
	}
	if !svc.authExpired.Load() {
		t.Fatal("authExpired = false, want true")
	}

	// Subsequent call returns ErrSessionExpired immediately without touching probe.
	svc.cookieRefreshMu.Lock()
	svc.lastCookieRefresh = time.Now().Add(-2 * time.Minute)
	svc.cookieRefreshMu.Unlock()
	if err := svc.maybeRefreshSession(context.Background()); !errors.Is(err, ytm.ErrSessionExpired) {
		t.Fatalf("post-expiry maybeRefreshSession = %v, want ErrSessionExpired", err)
	}
	if calls != 2 {
		t.Fatalf("probe called %d times after authExpired, want 2", calls)
	}
}
