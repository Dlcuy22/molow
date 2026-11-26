//go:build !linux && !windows

package loginwin

import "context"

// Open reports that this target has no login window.
func Open(Options) (*Session, error) {
	return nil, ErrUnsupported
}

// Session exists only so the shared call sites type check on this target.
type Session struct{}

// Wait always reports ErrUnsupported.
func (*Session) Wait(context.Context) (Result, error) { return Result{}, ErrUnsupported }

// Close does nothing.
func (*Session) Close() {}

func (*Session) currentURL() string { return "" }
