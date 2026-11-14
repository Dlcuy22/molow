// The log file is an optional second sink for slog's output. The console keeps
// its tidy treatment; the file gets every record, repeats included, as plain
// text. The state is global because slog's default logger is, and the service
// re-arms it from the settings document at startup and on every save.

package backend

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// logSink holds the console chain the app registered and, once armed, the file
// records are fanned out to.
var logSink struct {
	mu      sync.Mutex
	console slog.Handler
	file    *os.File
	armed   bool
	path    string
}

// registerConsoleLogHandler records the console chain the app built at
// startup, so a later arm can rebuild the default logger around it instead of
// replacing it.
func registerConsoleLogHandler(console slog.Handler) {
	logSink.mu.Lock()
	logSink.console = console
	logSink.mu.Unlock()
}

// logFilePath resolves the file the sink writes to: the user's path, made
// absolute so the panel's field and the log line name the same file, or the
// default molow.log in the config folder.
func logFilePath(path string) (string, error) {
	if path != "" {
		if filepath.IsAbs(path) {
			return path, nil
		}

		return filepath.Abs(path)
	}
	p, err := settingsPath()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(p), "molow.log"), nil
}

// applyLogFileSettings arms or disarms the sink from the settings document.
// It runs at startup and on every save, so the file follows the panel without
// a restart; a failure only costs the file, never the console.
func (s *PlayerService) applyLogFileSettings() {
	s.appSettingsMu.Lock()
	enabled := s.appSettings.General.LogFileEnabled
	path := s.appSettings.General.LogFile
	s.appSettingsMu.Unlock()

	if err := armLogFile(enabled, path); err != nil {
		s.log.Warn("log file: could not arm", "error", err)
	}
}

// fanoutHandler sends every record to both handlers. The file is built at the
// level the console's environment allows, so a MOLOW_DEBUG run captures the
// debug lines in the file too.
type fanoutHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return f.console.Enabled(ctx, level) || f.file.Enabled(ctx, level)
}

// Handle delivers the record to both sinks. The console gets a clone because
// Handle implementations may retain the record they are given; the file gets
// the original, which is the last handler that sees it.
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	errC := f.console.Handle(ctx, r.Clone())
	errF := f.file.Handle(ctx, r)
	if errC != nil {
		return errC
	}

	return errF
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanoutHandler{console: f.console.WithAttrs(attrs), file: f.file.WithAttrs(attrs)}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	return fanoutHandler{console: f.console.WithGroup(name), file: f.file.WithGroup(name)}
}

// armLogFile points the default logger at the console chain and, when enabled,
// at the log file as well. The swap is atomic, so a record in flight lands in
// one sink or the other; the worst a settings change can cost is one record
// that still held the old logger when its file was closed. An arm that changes
// nothing does not reopen the file, so a panel save that touches another
// setting leaves the sink alone.
func armLogFile(enabled bool, path string) error {
	logSink.mu.Lock()
	defer logSink.mu.Unlock()

	if !enabled {
		if !logSink.armed {
			return nil
		}
		logSink.armed = false
		logSink.path = ""
		if logSink.console != nil {
			slog.SetDefault(slog.New(logSink.console))
		}
		if logSink.file != nil {
			_ = logSink.file.Close()
			logSink.file = nil
		}

		return nil
	}

	p, err := logFilePath(path)
	if err != nil {
		return err
	}
	if logSink.armed && p == logSink.path {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if os.Getenv("MOLOW_DEBUG") != "" {
		level = slog.LevelDebug
	}
	fileHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})

	var next slog.Handler = fileHandler
	if logSink.console != nil {
		next = fanoutHandler{console: logSink.console, file: fileHandler}
	}
	slog.SetDefault(slog.New(next))
	slog.Info("log file: writing", "path", p)

	if logSink.file != nil {
		_ = logSink.file.Close()
	}
	logSink.file = f
	logSink.armed = true
	logSink.path = p

	return nil
}
