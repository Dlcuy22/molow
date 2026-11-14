package backend

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
