//go:build linux && cgo && !server && !gtk3

package backend

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

static void molo_set_window_icon_name(const char *name) {
	gtk_window_set_default_icon_name(name);
}
*/
import "C"

import "unsafe"

// setWindowIconName points GTK at the theme icon named by the desktop entry.
// GTK4 has no per-window icon bytes on Wayland: KWin draws the titlebar icon
// from the client's toplevel icon, so leaving this unset keeps the generic
// Wayland mark there even when the taskbar resolves the .desktop correctly.
func setWindowIconName(name string) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	C.molo_set_window_icon_name(cName)
}
