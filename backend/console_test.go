package backend

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dlcuy22/molo"
	"github.com/lmittmann/tint"
)

// dropTime removes the time attribute, so an assertion compares only the
// level, message and attributes the test pins.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}

// TestLogDebugEvent pins the engine record shape through slog: the record's
// sentence as the message, the kind as an attribute, the resolved components
// only when the record names them, and a failure at Error level. A change here
// is a deliberate edit to what a user reads.
func TestLogDebugEvent(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	// The time prefix is handler output, not part of the pinned shape, so it
	// is dropped here.
	slog.SetDefault(slog.New(tint.NewTextHandler(&buf, &tint.Options{
		NoColor:     true,
		ReplaceAttr: dropTime,
	})))
	defer slog.SetDefault(prev)

	cases := []struct {
		name string
		ev   molo.DebugEvent
		want string
	}{
		{
			name: "track names its resolved components",
			ev: molo.DebugEvent{
				Kind:    molo.DebugTrack,
				Path:    "/music/a.opus",
				Message: "track /music/a.opus",
				Count:   1000,
				Decoder: "go-opus",
				Parser:  "molo/decode (oggopus)",
				Backend: "oto",
			},
			want: "INF track /music/a.opus kind=track count=1000 decoder=go-opus parser=\"molo/decode (oggopus)\" backend=oto",
		},
		{
			name: "a bare record keeps its path",
			ev: molo.DebugEvent{
				Kind:    molo.DebugState,
				Path:    "/music/a.opus",
				Message: "idle -> playing",
			},
			want: "INF idle -> playing kind=state path=/music/a.opus",
		},
		{
			name: "underrun carries its totals",
			ev: molo.DebugEvent{
				Kind:     molo.DebugUnderrun,
				Path:     "/music/a.opus",
				Message:  "underrun +2",
				Delta:    2,
				Count:    5,
				Buffered: 4096,
			},
			want: "INF underrun +2 kind=underrun path=/music/a.opus count=5 buffered=4096",
		},
		{
			name: "an engine failure logs at Error",
			ev: molo.DebugEvent{
				Kind:    molo.DebugError,
				Path:    "/music/a.opus",
				Message: "decoder exhausted",
			},
			want: "ERR decoder exhausted kind=error path=/music/a.opus",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			logDebugEvent(tc.ev)
			if got := strings.TrimSpace(buf.String()); got != tc.want {
				t.Fatalf("logDebugEvent =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestPumpDebugWritesEachRecord proves the pump drains the debug stream to
// slog and stops on close, which is the whole bridge between the engine's
// consumer-gated stream and the terminal.
func TestPumpDebugWritesEachRecord(t *testing.T) {
	ch := make(chan molo.DebugEvent, 2)
	ch <- molo.DebugEvent{Kind: molo.DebugState, Message: "idle -> playing"}
	ch <- molo.DebugEvent{Kind: molo.DebugError, Message: "boom"}
	close(ch)

	f := newFakePlayer()
	f.debugCh = ch

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(tint.NewTextHandler(&buf, &tint.Options{NoColor: true})))
	defer slog.SetDefault(prev)

	svc := &PlayerService{player: f, closing: make(chan struct{})}
	svc.wg.Add(1)

	done := make(chan struct{})
	go func() {
		svc.pumpDebug()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpDebug did not return after the stream closed")
	}

	out := buf.String()
	if !strings.Contains(out, "idle -> playing") || !strings.Contains(out, "boom") {
		t.Fatalf("pumpDebug output missing a record:\n%s", out)
	}
}
