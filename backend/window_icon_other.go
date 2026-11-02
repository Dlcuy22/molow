//go:build !linux || server || !cgo || gtk3

package backend

// setWindowIconName is a no-op where GTK does not own the window icon.
func setWindowIconName(string) {}
