package backend

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lmittmann/tint"
)

// newTidyTestLogger points slog at a tidy handler over a plain tint handler
// writing to buf, and returns the flush and a restore. The time attribute is
// dropped, because it is handler output, not part of the pinned shape.
func newTidyTestLogger(t *testing.T, buf *bytes.Buffer) (flush func(), restore func()) {
	t.Helper()

	prev := slog.Default()
	slog.SetDefault(slog.New(newTidyHandler(tint.NewTextHandler(buf, &tint.Options{
		Level:       slog.LevelDebug,
		NoColor:     true,
		ReplaceAttr: dropTime,
	}))))
	return func() {
		if h, ok := slog.Default().Handler().(*tidyHandler); ok {
			_ = h.Flush()
		}
	}, func() { slog.SetDefault(prev) }
}

// activitySet logs the record shape the Discord presence re-emits every few
// seconds, twice, so a change between the pair is visible in the output.
func activitySet(large string) {
	slog.Debug("discordrpc: activity set",
		"title", "Dramaturgy",
		"large", large,
		"small", "ongoplayer_small",
	)
}

// TestTidyCollapsesRepeats pins the collapse: identical consecutive records
// become one line with a repeat count, a record that differs in an attribute
// stands on its own, and the last held record is sent on by Flush.
func TestTidyCollapsesRepeats(t *testing.T) {
	var buf bytes.Buffer
	flush, restore := newTidyTestLogger(t, &buf)
	defer restore()

	activitySet("ongoplayer")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	activitySet("https://tmpfiles.org/dl/art.jpg")
	slog.Info("lyrics: chain started", "path", "ytm:dEfl-ObuapA")
	flush()

	out := strings.TrimSpace(buf.String())
	want := strings.Join([]string{
		`DBG discordrpc: activity set title=Dramaturgy large=ongoplayer small=ongoplayer_small`,
		`DBG discordrpc: activity set title=Dramaturgy large=https://tmpfiles.org/dl/art.jpg small=ongoplayer_small repeat=6x`,
		`INF lyrics: chain started path=ytm:dEfl-ObuapA`,
	}, "\n")
	if out != want {
		t.Fatalf("tidy output =\n  %q\nwant\n  %q", out, want)
	}
}

// TestTidyCountAfterTheLastRepeat covers the end-of-stream case: repeats with
// nothing after them still get their count, because Flush sends the held
// record on.
func TestTidyCountAfterTheLastRepeat(t *testing.T) {
	var buf bytes.Buffer
	flush, restore := newTidyTestLogger(t, &buf)
	defer restore()

	activitySet("ongoplayer")
	activitySet("ongoplayer")
	activitySet("ongoplayer")
	flush()

	out := strings.TrimSpace(buf.String())
	if out != `DBG discordrpc: activity set title=Dramaturgy large=ongoplayer small=ongoplayer_small repeat=3x` {
		t.Fatalf("tidy output =\n  %q", out)
	}
}

// TestTidyDisabledKeepsEveryLine proves MOLOW_DEBUG_TIDY=0 hands every record
// to the next handler, because a tool collecting the log wants the raw stream.
func TestTidyDisabledKeepsEveryLine(t *testing.T) {
	t.Setenv("MOLOW_DEBUG_TIDY", "0")

	var buf bytes.Buffer
	flush, restore := newTidyTestLogger(t, &buf)
	defer restore()

	activitySet("ongoplayer")
	activitySet("ongoplayer")
	activitySet("ongoplayer")
	flush()

	if got := strings.Count(buf.String(), "discordrpc: activity set"); got != 3 {
		t.Fatalf("disabled tidy kept %d of 3 lines:\n%s", got, buf.String())
	}
}

// TestTidyKeepsTheFirstTime proves a collapsed line keeps the timestamp of its
// first occurrence, which is when the state it describes was reached. The
// records are handed to the handler directly, so slog does not stamp them.
func TestTidyKeepsTheFirstTime(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 3, 7, 0, time.UTC)

	var buf bytes.Buffer
	h := newTidyHandler(tint.NewTextHandler(&buf, &tint.Options{NoColor: true}))

	record := func(ts time.Time) slog.Record {
		r := slog.NewRecord(ts, slog.LevelDebug, "discordrpc: activity set", 0)
		r.AddAttrs(slog.String("title", "Dramaturgy"))
		return r
	}
	if err := h.Handle(context.Background(), record(at)); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), record(at.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), at.Format(time.StampMilli)) {
		t.Fatalf("collapsed line lost the first timestamp:\n%s", buf.String())
	}
}
