//go:build profile

package backend

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	profileEnvVar      = "MOLOW_PROFILE"
	profilePprofAddr   = "MOLOW_PPROF_ADDR"
	profileCDPPortEnv  = "MOLOW_CDP_PORT"
	defaultCDPPort     = 9222
	webview2ArgsEnvVar = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
)

type runtimeSnapshot struct {
	Timestamp    string `json:"timestamp"`
	Goroutines   int    `json:"goroutines"`
	HeapAlloc    uint64 `json:"heap_alloc_bytes"`
	HeapInuse    uint64 `json:"heap_inuse_bytes"`
	HeapSys      uint64 `json:"heap_sys_bytes"`
	StackInuse   uint64 `json:"stack_inuse_bytes"`
	NumGC        uint32 `json:"num_gc"`
	PauseTotalNs uint64 `json:"pause_total_ns"`
	NextGC       uint64 `json:"next_gc_bytes"`
}

// startProfiler arms the loopback pprof server and WebView2 remote debugging
// when MOLOW_PROFILE is set. It returns a shutdown hook for the HTTP server
// and any extra WebView2 browser arguments to pass via WindowsOptions.
func startProfiler() ([]string, func()) {
	if !isProfileEnabled(os.Getenv(profileEnvVar)) {
		return nil, func() {}
	}

	cdpPort := configureWebView2CDP()
	var browserArgs []string
	if cdpPort > 0 {
		browserArgs = []string{"--remote-debugging-port=" + strconv.Itoa(cdpPort)}
		slog.Info("profiler: cdp enabled", "port", cdpPort, "url", "http://127.0.0.1:"+strconv.Itoa(cdpPort))
	}

	addr := strings.TrimSpace(os.Getenv(profilePprofAddr))
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("profiler: pprof listen failed", "addr", addr, "error", err)
		return browserArgs, func() {}
	}

	srv := &http.Server{
		Handler:           newProfileMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("profiler: pprof listening", "addr", ln.Addr().String())
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("profiler: pprof server stopped", "error", err)
		}
	}()

	return browserArgs, func() {
		_ = srv.Close()
	}
}

func isProfileEnabled(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func configureWebView2CDP() int {
	existing := os.Getenv(webview2ArgsEnvVar)
	if port, ok := extractRemoteDebugPort(existing); ok {
		return port
	}

	port := defaultCDPPort
	if raw := strings.TrimSpace(os.Getenv(profileCDPPortEnv)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 65535 {
			port = parsed
		}
	} else {
		// Pick an ephemeral port if the default 9222 is already taken.
		if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
			port = ln.Addr().(*net.TCPAddr).Port
			_ = ln.Close()
		}
	}

	flag := "--remote-debugging-port=" + strconv.Itoa(port)
	if strings.TrimSpace(existing) != "" {
		flag = existing + " " + flag
	}
	_ = os.Setenv(webview2ArgsEnvVar, flag)
	return port
}

func extractRemoteDebugPort(args string) (int, bool) {
	for _, field := range strings.Fields(args) {
		if after, ok := strings.CutPrefix(field, "--remote-debugging-port="); ok {
			if port, err := strconv.Atoi(after); err == nil && port > 0 {
				return port, true
			}
		}
	}
	return 0, false
}

func newProfileMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/runtime", handleRuntimeSnapshot)
	return mux
}

func handleRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	snap := runtimeSnapshot{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		Goroutines:   runtime.NumGoroutine(),
		HeapAlloc:    ms.HeapAlloc,
		HeapInuse:    ms.HeapInuse,
		HeapSys:      ms.HeapSys,
		StackInuse:   ms.StackInuse,
		NumGC:        ms.NumGC,
		PauseTotalNs: ms.PauseTotalNs,
		NextGC:       ms.NextGC,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
