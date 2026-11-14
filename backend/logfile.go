// The log sinks are global because slog's default logger is. The console chain
// the app registered is rebuilt when the level moves, and the optional log
// file is fanned out beside it; the service re-applies both from the settings
// document at startup and on every save.

package backend

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

// logSink holds the console chain, the level it was built at, and, once armed,
// the file records are fanned out to.
var logSink struct {
	mu          sync.Mutex
	console     slog.Handler
	level       slog.Level
	file        *os.File
	fileHandler slog.Handler
	fileLevel   slog.Level
	armed       bool
	path        string
}

// resolveLogLevel maps the persisted choice to a level. MOLOW_DEBUG stays an
// escape hatch above it: a debug run is verbose whatever the panel says.
func resolveLogLevel(setting string) slog.Level {
	if setting == "debug" || os.Getenv("MOLOW_DEBUG") != "" {
		return slog.LevelDebug
	}

	return slog.LevelInfo
}

// consoleHandler builds the stderr chain for a level: tinted on a terminal,
// plain when piped, and collapsed when tidy is on.
func consoleHandler(level slog.Level) *tidyHandler {
	tty := isatty.IsTerminal(os.Stderr.Fd())

	return newTidyHandler(tint.NewTextHandler(os.Stderr, &tint.Options{
		Level:      level,
		NoColor:    !tty || os.Getenv("NO_COLOR") != "",
		TimeFormat: time.Kitchen,
	}))
}

// registerConsoleLogHandler records the console chain the app built at
// startup, so a later apply can rebuild the default logger around it instead
// of replacing it.
func registerConsoleLogHandler(console slog.Handler, level slog.Level) {
	logSink.mu.Lock()
	logSink.console = console
	logSink.level = level
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

// fanoutHandler sends every record to the sinks that take its level. The
// console and the file can sit at different levels, so each is asked before it
// is handed a record.
type fanoutHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return f.console.Enabled(ctx, level) || f.file.Enabled(ctx, level)
}

// Handle delivers the record to each sink that takes the level. The console
// gets a clone because Handle implementations may retain the record they are
// given; the file gets the original, which is the last handler that sees it.
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	if f.console.Enabled(ctx, r.Level) {
		if err := f.console.Handle(ctx, r.Clone()); err != nil {
			first = err
		}
	}
	if f.file.Enabled(ctx, r.Level) {
		if err := f.file.Handle(ctx, r); err != nil && first == nil {
			first = err
		}
	}

	return first
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanoutHandler{console: f.console.WithAttrs(attrs), file: f.file.WithAttrs(attrs)}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	return fanoutHandler{console: f.console.WithGroup(name), file: f.file.WithGroup(name)}
}

// setDefaultLocked rebuilds the default logger from the current state. Tests
// run with no registered console chain, so they get the file alone; an apply
// with nothing to point at leaves the installed default alone.
func setDefaultLocked() {
	var next slog.Handler
	switch {
	case logSink.console != nil && logSink.armed && logSink.fileHandler != nil:
		next = fanoutHandler{console: logSink.console, file: logSink.fileHandler}
	case logSink.console != nil:
		next = logSink.console
	case logSink.armed && logSink.fileHandler != nil:
		next = logSink.fileHandler
	default:
		return
	}
	slog.SetDefault(slog.New(next))
}

// applyLog points the default logger at the console chain and, when enabled,
// at the log file as well, both at level. A level move rebuilds the console
// chain, so the panel's choice moves the console too, not only the file. The
// swap is atomic, so a record in flight lands in one sink or the other; the
// worst an apply can cost is one record that still held the old logger when
// its file was closed. An apply that changes nothing does not reopen the
// file, so a panel save that touches another setting leaves the sinks alone.
func applyLog(level slog.Level, enabled bool, path string) error {
	logSink.mu.Lock()
	defer logSink.mu.Unlock()

	consoleRebuilt := false
	if logSink.console != nil && level != logSink.level {
		logSink.console = consoleHandler(level)
		consoleRebuilt = true
	}
	logSink.level = level

	if !enabled {
		if logSink.file != nil {
			_ = logSink.file.Close()
			logSink.file = nil
		}
		logSink.fileHandler = nil
		logSink.armed = false
		logSink.path = ""
		setDefaultLocked()

		return nil
	}

	p, err := logFilePath(path)
	if err != nil {
		return err
	}
	// The file handler cannot change its level in place, so a level move or a
	// new path rebuilds it around a fresh open. Nothing moved means the open
	// file stays.
	if logSink.armed && p == logSink.path && level == logSink.fileLevel {
		if consoleRebuilt {
			setDefaultLocked()
		}

		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	if logSink.file != nil {
		_ = logSink.file.Close()
	}
	logSink.file = f
	logSink.fileHandler = slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})
	logSink.fileLevel = level
	logSink.armed = true
	logSink.path = p
	setDefaultLocked()
	slog.Info("log file: writing", "path", p)

	return nil
}

// applyLogSettings applies the level and the log file from the settings
// document. It runs at startup and on every save, so both follow the panel
// without a restart; a failure only costs the file, never the console.
func (s *PlayerService) applyLogSettings() {
	s.appSettingsMu.Lock()
	enabled := s.appSettings.General.LogFileEnabled
	path := s.appSettings.General.LogFile
	setting := s.appSettings.General.LogLevel
	s.appSettingsMu.Unlock()

	if err := applyLog(resolveLogLevel(setting), enabled, path); err != nil {
		s.log.Warn("log file: could not apply", "error", err)
	}
}
