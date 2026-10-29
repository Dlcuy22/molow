//go:build !linux

package loginwin

import "context"

// Open reports that this target has no login window.
//
// Windows and macOS need their own cookie plumbing: Windows has WebView2's
// ICoreWebView2CookieManager inside Wails' own internal package, and macOS needs
// WKHTTPCookieStore through purego/objc. Neither is implemented, so this stub
// keeps the rest of the backend compiling on those targets instead of pretending
// to work.
func Open(Options) (*Session, error) {
	return nil, ErrUnsupported
}

// Session exists only so the shared call sites type check on this target.
type Session struct{}

// Wait always reports ErrUnsupported.
func (*Session) Wait(context.Context) (Result, error) { return Result{}, ErrUnsupported }

// Restore always reports ErrUnsupported.
func (*Session) Restore([]Cookie) error { return ErrUnsupported }

// Close does nothing.
func (*Session) Close() {}
