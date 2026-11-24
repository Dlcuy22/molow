//go:build profile

package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIsProfileEnabled(t *testing.T) {
	for _, truthy := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		if !isProfileEnabled(truthy) {
			t.Fatalf("isProfileEnabled(%q) = false, want true", truthy)
		}
	}
	for _, falsy := range []string{"", "0", "false", "no", "off"} {
		if isProfileEnabled(falsy) {
			t.Fatalf("isProfileEnabled(%q) = true, want false", falsy)
		}
	}
}

func TestProfileMuxEndpoints(t *testing.T) {
	srv := httptest.NewServer(newProfileMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/runtime")
	if err != nil {
		t.Fatalf("GET /debug/runtime: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var snap runtimeSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode runtimeSnapshot: %v", err)
	}
	if snap.Goroutines <= 0 {
		t.Fatalf("goroutines = %d, want > 0", snap.Goroutines)
	}
	if snap.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}

	pprofResp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer pprofResp.Body.Close()
	if pprofResp.StatusCode != http.StatusOK {
		t.Fatalf("pprof index status = %d, want 200", pprofResp.StatusCode)
	}
}

func TestConfigureWebView2CDP(t *testing.T) {
	t.Setenv(webview2ArgsEnvVar, "--disable-gpu")
	t.Setenv(profileCDPPortEnv, "9333")

	port := configureWebView2CDP()
	if port != 9333 {
		t.Fatalf("port = %d, want 9333", port)
	}
	got := os.Getenv(webview2ArgsEnvVar)
	if !strings.Contains(got, "--disable-gpu") || !strings.Contains(got, "--remote-debugging-port=9333") {
		t.Fatalf("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = %q", got)
	}

	// An existing --remote-debugging-port in the environment wins over MOLOW_CDP_PORT.
	if existing := configureWebView2CDP(); existing != 9333 {
		t.Fatalf("reused port = %d, want 9333", existing)
	}
}
