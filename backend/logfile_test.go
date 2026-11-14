package backend

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlcuy22/molo"
)

// TestLogFileArmsAndCaptures pins the sink: arming creates the file, a record
// lands in it, and disarming closes it. The service keeps no handle of its
// own, so the test drives applyLog the way the service does.
func TestLogFileArmsAndCaptures(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelInfo, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	slog.Info("log file: test line")

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "log file: test line") {
		t.Fatalf("the record did not reach the file: %q", raw)
	}

	if err := applyLog(slog.LevelInfo, false, ""); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the file did not survive disarming: %v", err)
	}
}

// TestLogFileFollowsTheLevel pins the level split: an Info sink drops a debug
// record, and moving the level to Debug keeps it. Both sinks move together.
func TestLogFileFollowsTheLevel(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelInfo, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	slog.Debug("log file: dropped debug line")

	if err := applyLog(slog.LevelDebug, true, p); err != nil {
		t.Fatalf("apply at debug: %v", err)
	}
	slog.Debug("log file: kept debug line")

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "dropped debug line") {
		t.Fatal("an Info sink captured a debug record")
	}
	if !strings.Contains(body, "kept debug line") {
		t.Fatal("a Debug sink dropped its own debug record")
	}
}

// TestLogFileKeepsEveryRepeat pins the fanout split: the console chain
// collapses consecutive repeats into one counted line, but the file is fed
// outside that chain, so a diagnostic run keeps every record.
func TestLogFileKeepsEveryRepeat(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	var console bytes.Buffer
	registerConsoleLogHandler(newTidyHandler(slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo})), slog.LevelInfo)

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelInfo, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for i := 0; i < 3; i++ {
		slog.Info("log file: same record")
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(raw), "log file: same record"); got != 3 {
		t.Fatalf("repeats = %d, want 3 uncollapsed lines", got)
	}
}

// TestLogRedirectKeepsCapturedLoggersLive pins the stale-logger fix: a logger
// captured before an apply follows the new sinks, which is what the service's
// own s.log does across a settings save.
func TestLogRedirectKeepsCapturedLoggersLive(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		logRedirect.Store(nil)
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	var console bytes.Buffer
	tidy := newTidyHandler(slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo}))
	registerConsoleLogHandler(tidy, slog.LevelInfo)
	installLogRedirect(tidy)

	// What the service captures at construction, before the file exists.
	captured := slog.Default()

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelInfo, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	captured.Info("log file: captured line")

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "log file: captured line") {
		t.Fatalf("a captured logger went stale: %q", raw)
	}
}

// TestWailsLogFloorsDebug pins the wails floor: a debug run keeps the app's
// debug records but wails' own debug lines (its per-request chatter) stay
// out, while its info lines flow like any other record.
func TestWailsLogFloorsDebug(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		logRedirect.Store(nil)
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	installLogRedirect(slog.DiscardHandler)
	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelDebug, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	wails := slog.New(wailsLogHandler{base: slog.Default().Handler()})
	wails.Debug("handleWebViewRequest: Processing request")
	wails.Info("Build Info")
	slog.Debug("discordrpc: activity set")

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "handleWebViewRequest") {
		t.Fatal("a wails debug record reached the file")
	}
	if !strings.Contains(body, "Build Info") {
		t.Fatal("a wails info record was dropped")
	}
	if !strings.Contains(body, "discordrpc: activity set") {
		t.Fatal("an app debug record was dropped")
	}
}

// TestStartDebugPumpRunsUnconditionally pins the double-click fix: the pump
// starts with no console attached, so the engine's state and track records
// reach the log file on a GUI launch, not only a terminal run.
func TestStartDebugPumpRunsUnconditionally(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		logRedirect.Store(nil)
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	installLogRedirect(slog.DiscardHandler)
	p := filepath.Join(t.TempDir(), "molow.log")
	if err := applyLog(slog.LevelInfo, true, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ch := make(chan molo.DebugEvent, 1)
	ch <- molo.DebugEvent{Kind: molo.DebugState, Message: "idle -> playing"}

	f := newFakePlayer()
	f.debugCh = ch
	svc := &PlayerService{player: f, closing: make(chan struct{})}
	// A GUI launch: no console was attached, and the pump starts anyway.
	svc.startDebugPump()

	if err := waitFor(t, func() error {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(raw), "idle -> playing") {
			return fmt.Errorf("the engine record did not reach the file: %q", raw)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	close(svc.closing)
	svc.wg.Wait()
}

// waitFor polls probe until it returns nil or the deadline passes, so a
// background pump has its turn to write.
func waitFor(t *testing.T, probe func() error) error {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := probe(); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestClearLogFileTruncatesInPlace pins the clear: the file is emptied by
// path while the sink keeps its append handle, the old records are gone, and
// the next record starts at offset zero.
func TestClearLogFileTruncatesInPlace(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		logRedirect.Store(nil)
		registerConsoleLogHandler(nil, slog.LevelInfo)
		_ = applyLog(slog.LevelInfo, false, "")
		slog.SetDefault(previous)
	})

	installLogRedirect(slog.DiscardHandler)
	if err := applyLog(slog.LevelInfo, true, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	slog.Info("log file: before clear")
	slog.Info("log file: before clear two")

	svc := &PlayerService{closing: make(chan struct{})}
	got, err := svc.ClearLogFile()
	if err != nil {
		t.Fatalf("ClearLogFile: %v", err)
	}
	if got.Files != 1 || got.Bytes == 0 {
		t.Fatalf("clear reported %d files and %d bytes, want 1 and a size", got.Files, got.Bytes)
	}

	slog.Info("log file: after clear")

	raw, err := os.ReadFile(logSink.path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "before clear") {
		t.Fatal("the clear left old records behind")
	}
	if !strings.Contains(body, "log file: cleared") || !strings.Contains(body, "after clear") {
		t.Fatalf("the truncated file stopped taking records: %q", body)
	}
}

// TestClearLogFileOnAMissingFileIsEmpty pins the disabled case: with the file
// gone from disk the clear reports nothing removed instead of an error.
func TestClearLogFileOnAMissingFileIsEmpty(t *testing.T) {
	isolateConfig(t)

	svc := &PlayerService{closing: make(chan struct{})}
	got, err := svc.ClearLogFile()
	if err != nil {
		t.Fatalf("ClearLogFile: %v", err)
	}
	if got.Files != 0 || got.Bytes != 0 {
		t.Fatalf("clear reported %d files and %d bytes on a missing file", got.Files, got.Bytes)
	}
}

// TestResolveLogLevel pins the mapping: the panel's debug, and MOLOW_DEBUG
// over any choice, are the two ways to reach debug level.
func TestResolveLogLevel(t *testing.T) {
	if got := resolveLogLevel("info"); got != slog.LevelInfo {
		t.Fatalf("info = %v, want LevelInfo", got)
	}
	if got := resolveLogLevel(""); got != slog.LevelInfo {
		t.Fatalf("empty = %v, want LevelInfo", got)
	}
	if got := resolveLogLevel("debug"); got != slog.LevelDebug {
		t.Fatalf("debug = %v, want LevelDebug", got)
	}

	t.Setenv("MOLOW_DEBUG", "1")
	if got := resolveLogLevel("info"); got != slog.LevelDebug {
		t.Fatalf("MOLOW_DEBUG over info = %v, want LevelDebug", got)
	}
}

// TestLogFilePathDefaultsToTheConfigFolder pins the empty field: the default
// location is molow.log beside the settings document.
func TestLogFilePathDefaultsToTheConfigFolder(t *testing.T) {
	root := isolateConfig(t)

	got, err := logFilePath("")
	if err != nil {
		t.Fatalf("logFilePath: %v", err)
	}
	want := filepath.Join(root, "molow", "molow.log")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}

	// A relative path is resolved against the working directory, so the file
	// the app opens is the one the log line names.
	rel, err := logFilePath("logs/molow.log")
	if err != nil {
		t.Fatalf("logFilePath: %v", err)
	}
	if !filepath.IsAbs(rel) {
		t.Fatalf("relative path = %q, want it resolved to an absolute one", rel)
	}
}
