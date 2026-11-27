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

	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
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

func TestCookieCapturingTransportPersistsRotatedCookiesPerLibrary(t *testing.T) {
	isolateConfig(t)

	svc := &PlayerService{ytmProv: newYTMProvider(newYTMIndex(), nil, false, nil)}
	cs, err := svc.credentials()
	if err != nil {
		t.Fatal(err)
	}
	mgr := svc.ytmManager()

	credsA := &Credentials{CookieHeader: "SAPISID=seedA; SID=sidA; SIDCC=oldA", Name: "Account A"}
	credsB := &Credentials{CookieHeader: "SAPISID=seedB; SID=sidB; SIDCC=oldB", Name: "Account B"}
	if err := cs.Save("cred_a", library.KindYTM, credsA); err != nil {
		t.Fatal(err)
	}
	if err := cs.Save("cred_b", library.KindYTM, credsB); err != nil {
		t.Fatal(err)
	}

	sessA := mgr.ensureSession("ytm_a", "cred_a", ytmLibraryConfig{AccountName: "Account A"})
	_ = mgr.ensureSession("ytm_b", "cred_b", ytmLibraryConfig{AccountName: "Account B"})
	genA := sessA.authGen.Add(1)

	rt := &cookieCapturingTransport{
		libraryID: "ytm_a",
		gen:       genA,
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Set-Cookie": {"SIDCC=rotatedA; Path=/; Domain=.youtube.com"},
				},
				Body: io.NopCloser(bytes.NewBufferString("{}")),
			}, nil
		}),
		onSetCookies: mgr.applyRotatedCookies,
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

	var gotA, gotB Credentials
	if err := cs.Load("cred_a", library.KindYTM, &gotA); err != nil {
		t.Fatalf("Load cred_a: %v", err)
	}
	if err := cs.Load("cred_b", library.KindYTM, &gotB); err != nil {
		t.Fatalf("Load cred_b: %v", err)
	}
	if !strings.Contains(gotA.CookieHeader, "SIDCC=rotatedA") {
		t.Fatalf("cred_a CookieHeader = %q, want rotated SIDCC", gotA.CookieHeader)
	}
	if !strings.Contains(gotB.CookieHeader, "SIDCC=oldB") {
		t.Fatalf("cred_b CookieHeader = %q was mutated unexpectedly", gotB.CookieHeader)
	}
}

func TestApplyRotatedCookiesGenerationGuardAfterSignOut(t *testing.T) {
	isolateConfig(t)
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := &PlayerService{
		libStore: store,
		libs:     map[string]library.Source{},
		ytmProv:  newYTMProvider(newYTMIndex(), nil, false, nil),
	}
	cs, err := svc.credentials()
	if err != nil {
		t.Fatal(err)
	}
	mgr := svc.ytmManager()

	if err := cs.Save("cred_1", library.KindYTM, &Credentials{CookieHeader: "SAPISID=seed1; SID=sid1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddLibraryRow(sqlite.LibraryRow{
		Library:      library.Library{ID: "ytm_1", Kind: library.KindYTM, Name: "YTM 1"},
		CredentialID: "cred_1",
	}); err != nil {
		t.Fatal(err)
	}
	sess := mgr.ensureSession("ytm_1", "cred_1", ytmLibraryConfig{})
	staleGen := sess.authGen.Add(1)

	if err := svc.SignOutLibrary("ytm_1", false); err != nil {
		t.Fatalf("SignOutLibrary: %v", err)
	}

	// A late Set-Cookie callback carrying the pre-sign-out generation must be ignored
	// and must not recreate cred_1.enc.
	mgr.applyRotatedCookies("ytm_1", staleGen, []string{"SIDCC=zombie; Path=/; Domain=.youtube.com"})

	var check Credentials
	if err := cs.Load("cred_1", library.KindYTM, &check); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Load after stale callback = %v, want ErrNoCredentials", err)
	}
}

func TestMaybeRefreshSessionThrottleAndExpiry(t *testing.T) {
	isolateConfig(t)

	svc := &PlayerService{ytmProv: newYTMProvider(newYTMIndex(), nil, false, nil)}
	cs, err := svc.credentials()
	if err != nil {
		t.Fatal(err)
	}
	mgr := svc.ytmManager()

	if err := cs.Save("cred_1", library.KindYTM, &Credentials{CookieHeader: "SAPISID=seed1; SID=sid1"}); err != nil {
		t.Fatal(err)
	}
	sess := mgr.ensureSession("ytm_1", "cred_1", ytmLibraryConfig{})
	sess.authGen.Store(1)

	calls := 0
	returnExpired := false
	mgr.refreshSessionFn = func(ctx context.Context, client *ytm.Client) error {
		calls++
		if returnExpired {
			return ytm.ErrSessionExpired
		}
		return nil
	}

	if err := mgr.maybeRefreshSession(context.Background(), "ytm_1"); err != nil {
		t.Fatalf("first maybeRefreshSession: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// Second call within 60 s must be throttled.
	if err := mgr.maybeRefreshSession(context.Background(), "ytm_1"); err != nil {
		t.Fatalf("throttled maybeRefreshSession: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after throttle = %d, want 1", calls)
	}

	// Expire cooldown, simulate revoked session -> sets sess.authExpired = true and stops retrying.
	sess.cookieRefreshMu.Lock()
	sess.lastCookieRefresh = time.Now().Add(-2 * time.Minute)
	sess.cookieRefreshMu.Unlock()
	returnExpired = true

	err = mgr.maybeRefreshSession(context.Background(), "ytm_1")
	if !errors.Is(err, ytm.ErrSessionExpired) {
		t.Fatalf("expired maybeRefreshSession = %v, want ErrSessionExpired", err)
	}
	if !sess.authExpired.Load() {
		t.Fatal("sess.authExpired = false, want true")
	}

	// Subsequent call returns ErrSessionExpired immediately without touching probe.
	sess.cookieRefreshMu.Lock()
	sess.lastCookieRefresh = time.Now().Add(-2 * time.Minute)
	sess.cookieRefreshMu.Unlock()
	if err := mgr.maybeRefreshSession(context.Background(), "ytm_1"); !errors.Is(err, ytm.ErrSessionExpired) {
		t.Fatalf("post-expiry maybeRefreshSession = %v, want ErrSessionExpired", err)
	}
	if calls != 2 {
		t.Fatalf("probe called %d times after authExpired, want 2", calls)
	}
}
