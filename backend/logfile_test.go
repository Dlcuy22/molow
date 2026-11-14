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
// own, so the test drives armLogFile the way the service does.
func TestLogFileArmsAndCaptures(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		registerConsoleLogHandler(nil)
		_ = armLogFile(false, "")
		slog.SetDefault(previous)
	})

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := armLogFile(true, p); err != nil {
		t.Fatalf("arm: %v", err)
	}
	slog.Info("log file: test line")

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "log file: test line") {
		t.Fatalf("the record did not reach the file: %q", raw)
	}

	if err := armLogFile(false, ""); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the file did not survive disarming: %v", err)
	}
}

// TestLogFileKeepsEveryRepeat pins the fanout split: the console chain
// collapses consecutive repeats into one counted line, but the file is fed
// outside that chain, so a diagnostic run keeps every record.
func TestLogFileKeepsEveryRepeat(t *testing.T) {
	isolateConfig(t)

	previous := slog.Default()
	t.Cleanup(func() {
		registerConsoleLogHandler(nil)
		_ = armLogFile(false, "")
		slog.SetDefault(previous)
	})

	var console bytes.Buffer
	registerConsoleLogHandler(newTidyHandler(slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo})))

	p := filepath.Join(t.TempDir(), "molow.log")
	if err := armLogFile(true, p); err != nil {
		t.Fatalf("arm: %v", err)
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
