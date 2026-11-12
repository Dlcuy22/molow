package backend

import (
	"log/slog"
	"strings"

	"github.com/dlcuy22/molo"
)

// consoleAttached records whether main attached the process to a terminal. It
// is set once, before the logger is built, and read when the service starts the
// debug pump.
var consoleAttached bool

// startConsole starts the debug pump when the process has a terminal. It does
// nothing when there is none, which is the normal double-clicked GUI case: no
// console is allocated and the consumer-gated stream is never attached.
func (s *PlayerService) startConsole() {
	if !consoleAttached {
		return
	}

	// Attaching the console is what also makes these slog lines visible: the
	// default handler writes to os.Stderr, and attachConsole has pointed that
	// at the terminal.
	s.wg.Add(1)
	go s.pumpDebug()
}

// pumpDebug reads the debug stream until shutdown. The stream is
// consumer-gated, so this call is what turns it on, and it drops its oldest
// record when the logger falls behind, so a slow writer never stalls the
// engine.
func (s *PlayerService) pumpDebug() {
	defer s.wg.Done()

	events := s.player.DebugEvents()
	for {
		select {
		case <-s.closing:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			logDebugEvent(ev)
		}
	}
}

// logDebugEvent writes one engine record through slog at Info, or Error when
// the engine reports a failure. The sentence stays the message; the fields it
// does not name travel as attributes.
func logDebugEvent(ev molo.DebugEvent) {
	attrs := []any{slog.String("kind", ev.Kind.String())}

	// The path and the elapsed time are usually already in the sentence, so
	// they are skipped there; a bare record such as "idle -> playing" keeps
	// its path.
	if ev.Path != "" && !strings.Contains(ev.Message, ev.Path) {
		attrs = append(attrs, slog.String("path", ev.Path))
	}
	if ev.Count != 0 {
		attrs = append(attrs, slog.Int64("count", ev.Count))
	}
	if ev.Buffered != 0 {
		attrs = append(attrs, slog.Int64("buffered", ev.Buffered))
	}
	if ev.Decoder != "" {
		attrs = append(attrs, slog.String("decoder", ev.Decoder))
	}
	if ev.Parser != "" {
		attrs = append(attrs, slog.String("parser", ev.Parser))
	}
	if ev.Backend != "" {
		attrs = append(attrs, slog.String("backend", ev.Backend))
	}

	if ev.Kind == molo.DebugError {
		slog.Error(ev.Message, attrs...)
		return
	}
	slog.Info(ev.Message, attrs...)
}
