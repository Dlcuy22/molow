package backend

import (
	"os"
	"path/filepath"
)

// loginJarDir is where the login window keeps its private profile: the WebKit
// data directory, the cache, and the cookie database.
//
// It sits under the config directory rather than a temporary one, because a
// GTK/WebKit profile on some filesystems fails to open from /tmp, and because the
// path is short enough not to trouble the Unix socket limit WebKit's own
// subprocesses inherit.
func loginJarDir() (string, error) {
	settings, err := settingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "login-jar"), nil
}

// resetLoginJar removes the profile directory and recreates it empty.
//
// Every sign-in starts from a clean profile. A leftover jar would make the
// capture depend on what happened last time, and the window could open already
// signed in with no way to tell a stale session from a fresh one.
func resetLoginJar(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}
