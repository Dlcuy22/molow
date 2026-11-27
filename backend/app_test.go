package backend

import (
	"os"
	"testing"
)

func TestConfigureWaylandEnvironment(t *testing.T) {
	// Helper to restore env vars
	origWayland := os.Getenv("WAYLAND_DISPLAY")
	origSession := os.Getenv("XDG_SESSION_TYPE")
	origDisable := os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER")
	t.Cleanup(func() {
		os.Setenv("WAYLAND_DISPLAY", origWayland)
		os.Setenv("XDG_SESSION_TYPE", origSession)
		if origDisable == "" {
			os.Unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER")
		} else {
			os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", origDisable)
		}
	})

	t.Run("sets WEBKIT_DISABLE_DMABUF_RENDERER when WAYLAND_DISPLAY is set", func(t *testing.T) {
		os.Setenv("WAYLAND_DISPLAY", "wayland-0")
		os.Unsetenv("XDG_SESSION_TYPE")
		os.Unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER")

		configureWaylandEnvironment()
		if got := os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER"); got != "1" {
			t.Fatalf("expected WEBKIT_DISABLE_DMABUF_RENDERER=1, got %q", got)
		}
	})

	t.Run("sets WEBKIT_DISABLE_DMABUF_RENDERER when XDG_SESSION_TYPE is wayland", func(t *testing.T) {
		os.Unsetenv("WAYLAND_DISPLAY")
		os.Setenv("XDG_SESSION_TYPE", "wayland")
		os.Unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER")

		configureWaylandEnvironment()
		if got := os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER"); got != "1" {
			t.Fatalf("expected WEBKIT_DISABLE_DMABUF_RENDERER=1, got %q", got)
		}
	})

	t.Run("preserves existing WEBKIT_DISABLE_DMABUF_RENDERER value", func(t *testing.T) {
		os.Setenv("WAYLAND_DISPLAY", "wayland-0")
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "0")

		configureWaylandEnvironment()
		if got := os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER"); got != "0" {
			t.Fatalf("expected WEBKIT_DISABLE_DMABUF_RENDERER=0 to be preserved, got %q", got)
		}
	})

	t.Run("does nothing under non-Wayland session", func(t *testing.T) {
		os.Unsetenv("WAYLAND_DISPLAY")
		os.Setenv("XDG_SESSION_TYPE", "x11")
		os.Unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER")

		configureWaylandEnvironment()
		if got := os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER"); got != "" {
			t.Fatalf("expected WEBKIT_DISABLE_DMABUF_RENDERER empty, got %q", got)
		}
	})
}
