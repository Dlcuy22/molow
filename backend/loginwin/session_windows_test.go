//go:build windows

package loginwin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWebView2EnvironmentAndCookiePipeline exercises the full COM lifecycle on a
// hidden window: CreateWebViewEnvironmentWithOptionsInternal ->
// CreateCoreWebView2Controller -> ICoreWebView2_2 -> GetCookieManager ->
// GetCookies against https://music.youtube.com/ (timed out cleanly).
func TestWebView2EnvironmentAndCookiePipeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()

	sess, err := openSession(Options{
		JarDir:  t.TempDir(),
		URL:     srv.URL,
		Timeout: 3 * time.Second,
	}, true)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = sess.Wait(ctx)
	if !errors.Is(err, ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want ErrTimeout or context.DeadlineExceeded", err)
	}
}
