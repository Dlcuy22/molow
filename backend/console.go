package backend

import (
	"log/slog"
	"strings"

	"github.com/dlcuy22/molo"
)

// startDebugPump mirrors the engine's debug stream into the app's sinks. The
// stream is consumer-gated, so this call is what turns it on, and it drops its
// oldest record when the logger falls behind, so a slow writer never stalls
// the engine. It runs on every launch: a terminal run watches the lines on
// stderr, and a GUI launch gets them in the log file. The pump once started
// only with a console attached, which is what left state and track changes
// out of the file on a double-clicked build.
func (s *PlayerService) startDebugPump() {
	s.wg.Add(1)
	go s.pumpDebug()
}

// pumpDebug reads the debug stream until shutdown.
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
