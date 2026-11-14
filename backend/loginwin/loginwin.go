// Package loginwin opens an isolated WebKitGTK window so a user can sign in to
// YouTube Music, then hands back the cookies that window collected.
//
// It exists because a Wails webview cannot do this job: the framework exposes no
// cookie API, and the cookies that matter are HttpOnly, so no script running in
// the page can read them either. Only the network layer can, which is why this
// package drives WebKitGTK directly.
//
// The window is a plain GTK4 toplevel on its own WebKitNetworkSession, created
// from the GTK main loop Wails already runs. It is built with purego rather than
// cgo so the package compiles under CGO_ENABLED=0 and every symbol stays in one
// file where it can be audited.
//
// Only Linux is implemented. The other targets return ErrUnsupported.
package loginwin

import (
	"errors"
	"time"
)

var (
	// ErrUnsupported means this build target has no login window.
	ErrUnsupported = errors.New("loginwin: not supported on this platform")

	// ErrClosed means the window went away before a session was captured.
	ErrClosed = errors.New("loginwin: window closed before sign in finished")

	// ErrTimeout means the wait deadline passed with no session cookie in the
	// jar.
	ErrTimeout = errors.New("loginwin: timed out waiting for sign in")
)

// DefaultLoginURL is where the window starts. Google sends the user to its own
// sign-in page and back to YouTube Music afterwards, which is why the cookie jar
// ends up holding cookies for several hosts.
const DefaultLoginURL = "https://accounts.google.com/ServiceLogin?continue=https%3A%2F%2Fmusic.youtube.com"

// Options configures one login window.
type Options struct {
	// JarDir is where the window keeps its own profile: the WebKit data
	// directory, the cache, and the cookie database. It is removed by the caller
	// after the capture.
	JarDir string

	// URL is the first page to load. Empty means DefaultLoginURL.
	URL string

	// Timeout bounds Wait. Zero means five minutes.
	Timeout time.Duration
}

// Cookie is one cookie as harvested from the window. It keeps the domain and the
// path, which a Cookie header throws away but Restore needs.
type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Secure   bool
	HTTPOnly bool
	SameSite string

	// Expires is zero for a session cookie. libsoup reports those with no
	// expiry at all, which is not the same as an already-expired one.
	Expires time.Time
}

// Result is what a completed sign-in yields.
type Result struct {
	// URL is the page the window was on when the capture happened.
	URL string

	// SAPISID is the cookie value that seeds the Authorization hash. It is
	// reported separately because its absence is the usual reason a capture is
	// useless.
	SAPISID string

	// CookieHeader is the single Cookie: header value, already filtered and
	// deduplicated. This is the form ytm-go replays verbatim.
	CookieHeader string

	// Cookies is the same set with domain and path intact, for Restore.
	Cookies []Cookie

	// VisitorData, DataSyncID and AuthUser are deliberately left empty here.
	// They are not cookies and the window cannot read them without running
	// script in the page. The caller fills them from the account listing, which
	// it has to call anyway to validate the session.
	VisitorData string
	DataSyncID  string
	AuthUser    string
}

// CurrentURL reports the page the window is showing.
//
// It exists for diagnostics: a capture that never completes looks identical to a
// hang, and knowing the page is the only way to tell a stalled Google flow from a
// window that never loaded at all.
func (s *Session) CurrentURL() string {
	return s.CurrentURL()
}
