//go:build linux

package loginwin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego"
)

// The smoke test needs a display and a running GTK loop, so it is opt-in.
//
//	LOGINWIN_SMOKE=1 go test ./backend/loginwin -run TestWindowHarvestsCookies -v
//
// It answers the two questions the plan could not: whether a purego window can
// harvest cookies at all, and whether WebKitGTK persists them across a restart.

// loopSymbols are the three extra glib/gtk calls the test drives directly.
var loopSymbols struct {
	gtkInit       func()
	gMainLoopNew  func(uintptr, int32) uintptr
	gMainLoopRun  func(uintptr)
	gMainLoopQuit func(uintptr)
}

func requireSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("LOGINWIN_SMOKE") == "" {
		t.Skip("set LOGINWIN_SMOKE=1 to run the window smoke test")
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display available")
	}
	if err := ensureLoaded(); err != nil {
		t.Fatalf("load libraries: %v", err)
	}
}

func registerLoopSymbols(t *testing.T) {
	t.Helper()
	gtk, err := openLib(libGTKNames)
	if err != nil {
		t.Fatalf("open gtk: %v", err)
	}
	glib, err := openLib(libGLibName)
	if err != nil {
		t.Fatalf("open glib: %v", err)
	}
	purego.RegisterLibFunc(&loopSymbols.gtkInit, gtk, "gtk_init")
	purego.RegisterLibFunc(&loopSymbols.gMainLoopNew, glib, "g_main_loop_new")
	purego.RegisterLibFunc(&loopSymbols.gMainLoopRun, glib, "g_main_loop_run")
	purego.RegisterLibFunc(&loopSymbols.gMainLoopQuit, glib, "g_main_loop_quit")
}

// cookieServer answers every request by setting the two cookies a session needs.
//
// The cookies carry an expiry on purpose. A cookie without one is a session
// cookie, and a browser drops those when it closes, so a persistence test built
// on them would measure the browser's correct behaviour rather than whether
// WebKitGTK writes its jar to disk. Google's auth cookies are long lived, so the
// expiring form is also the realistic one.
func cookieServer(t *testing.T, sapisid, sid string) *httptest.Server {
	t.Helper()
	expires := time.Now().Add(24 * time.Hour)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SAPISID", Value: sapisid, Path: "/", Expires: expires})
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: sid, Path: "/", Expires: expires})
		fmt.Fprint(w, "<html><body>cookie page</body></html>")
	}))
}

// waitForCookie polls the jar until the named cookie shows up.
func waitForCookie(ctx context.Context, s *Session, name string) (Cookie, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		cookies, err := s.harvest(ctx)
		if err != nil {
			return Cookie{}, err
		}
		for _, c := range cookies {
			if c.Name == name {
				return c, nil
			}
		}
		select {
		case <-ctx.Done():
			return Cookie{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func TestWindowHarvestsCookies(t *testing.T) {
	requireSmoke(t)
	registerLoopSymbols(t)

	// GTK must be initialised on, and driven from, one thread for the whole run.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	loopSymbols.gtkInit()
	loop := loopSymbols.gMainLoopNew(0, 0)
	if loop == 0 {
		t.Fatal("g_main_loop_new returned NULL")
	}

	jarDir := t.TempDir()
	server := cookieServer(t, "sapisid-from-the-window", "sid-from-the-window")
	defer server.Close()

	type outcome struct {
		cookie Cookie
		err    error
	}
	done := make(chan outcome, 1)

	go func() {
		s, err := Open(Options{JarDir: jarDir, URL: server.URL, Timeout: 30 * time.Second})
		if err != nil {
			done <- outcome{err: err}
			loopSymbols.gMainLoopQuit(loop)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cookie, err := waitForCookie(ctx, s, "SAPISID")
		s.Close()
		// Give the destroy job queued by Close a chance to run before the loop
		// stops, so the test does not leave a window behind.
		time.Sleep(200 * time.Millisecond)

		done <- outcome{cookie: cookie, err: err}
		loopSymbols.gMainLoopQuit(loop)
	}()

	loopSymbols.gMainLoopRun(loop)
	got := <-done

	if got.err != nil {
		t.Fatalf("harvest: %v", got.err)
	}
	if got.cookie.Value != "sapisid-from-the-window" {
		t.Errorf("SAPISID value = %q, want the value the page set", got.cookie.Value)
	}
	if !got.cookie.HTTPOnly && got.cookie.Value == "" {
		t.Error("cookie came back empty")
	}
	t.Logf("harvested SAPISID: domain=%q path=%q secure=%v httpOnly=%v",
		got.cookie.Domain, got.cookie.Path, got.cookie.Secure, got.cookie.HTTPOnly)

	// Persistence: a fresh session on the same data directory should see the
	// cookie without loading any page that sets it.
	verifyPersistence(t, jarDir)
}

// reportJarFiles lists what the data directory actually contains, so a
// persistence failure can be told apart from a jar that was never written.
func reportJarFiles(t *testing.T, jarDir string) {
	t.Helper()
	entries, err := os.ReadDir(jarDir)
	if err != nil {
		t.Logf("PERSISTENCE: cannot read %s: %v", jarDir, err)
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		t.Logf("PERSISTENCE: %s (%d bytes, dir=%v)", e.Name(), info.Size(), e.IsDir())
	}
	if len(entries) == 0 {
		t.Log("PERSISTENCE: the data directory is empty")
	}
}

// verifyPersistence answers the open question from the plan: does the cookie
// survive a close and reopen.
func verifyPersistence(t *testing.T, jarDir string) {
	t.Helper()

	reportJarFiles(t, jarDir)

	loop := loopSymbols.gMainLoopNew(0, 0)
	done := make(chan error, 1)

	go func() {
		// A page that sets nothing, so anything found came from the jar on disk.
		blank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "<html><body>blank</body></html>")
		}))
		defer blank.Close()

		s, err := Open(Options{JarDir: jarDir, URL: blank.URL, Timeout: 30 * time.Second})
		if err != nil {
			done <- err
			loopSymbols.gMainLoopQuit(loop)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_, err = waitForCookie(ctx, s, "SAPISID")
		s.Close()
		// The jar is flushed asynchronously; give it time to land before the
		// process moves on, otherwise the next read races the write.
		time.Sleep(2 * time.Second)
		done <- err
		loopSymbols.gMainLoopQuit(loop)
	}()

	loopSymbols.gMainLoopRun(loop)
	if err := <-done; err != nil {
		t.Logf("PERSISTENCE: the cookie did not survive a reopen (%v); Restore is the only path", err)
		reportJarFiles(t, jarDir)
		return
	}
	t.Log("PERSISTENCE: the cookie survived a close and reopen")
}

// TestWindowRestoresCookies proves the Restore path, which is what a relaunch
// uses when the jar on disk is missing or unusable.
func TestWindowRestoresCookies(t *testing.T) {
	requireSmoke(t)
	registerLoopSymbols(t)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	loopSymbols.gtkInit()
	loop := loopSymbols.gMainLoopNew(0, 0)
	if loop == 0 {
		t.Fatal("g_main_loop_new returned NULL")
	}

	jarDir := t.TempDir()
	blank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>blank</body></html>")
	}))
	defer blank.Close()

	// The host has to match the page origin, or the jar will not hand the cookie
	// back for it.
	host := strings.TrimPrefix(blank.URL, "http://")
	seeded := []Cookie{
		{Name: "SAPISID", Value: "restored-sapisid", Domain: host, Path: "/"},
		{Name: "SID", Value: "restored-sid", Domain: host, Path: "/"},
	}

	done := make(chan error, 1)
	go func() {
		s, err := Open(Options{JarDir: jarDir, URL: blank.URL, Timeout: 30 * time.Second})
		if err != nil {
			done <- err
			loopSymbols.gMainLoopQuit(loop)
			return
		}
		if err := s.Restore(seeded); err != nil {
			s.Close()
			done <- err
			loopSymbols.gMainLoopQuit(loop)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cookie, err := waitForCookie(ctx, s, "SAPISID")
		if err == nil && cookie.Value != "restored-sapisid" {
			err = fmt.Errorf("SAPISID = %q, want the restored value", cookie.Value)
		}
		s.Close()
		time.Sleep(200 * time.Millisecond)
		done <- err
		loopSymbols.gMainLoopQuit(loop)
	}()

	loopSymbols.gMainLoopRun(loop)
	if err := <-done; err != nil {
		t.Fatalf("Restore: %v", err)
	}
}
