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

// TestTidyEmitsImmediatelyAndSummarizesRepeats pins emit-first coalescing:
// every distinct record appears immediately without waiting for a later event
// or Flush, while consecutive duplicates collapse into one trailing repeat=+Nx
// summary when a different record arrives.
func TestTidyEmitsImmediatelyAndSummarizesRepeats(t *testing.T) {
	var buf bytes.Buffer
	flush, restore := newTidyTestLogger(t, &buf)
	defer restore()

	activitySet("ongoplayer")
	activitySet("https://tmpfiles.org/dl/art.jpg")

	// Both distinct records must already be visible before any follow-up event
	// or Flush so live logs never lag behind the active presence.
	mid := strings.TrimSpace(buf.String())
	wantMid := strings.Join([]string{
		`DBG discordrpc: activity set title=Dramaturgy large=ongoplayer small=ongoplayer_small`,
		`DBG discordrpc: activity set title=Dramaturgy large=https://tmpfiles.org/dl/art.jpg small=ongoplayer_small`,
	}, "\n")
	if mid != wantMid {
		t.Fatalf("live output before flush =\n  %q\nwant\n  %q", mid, wantMid)
	}

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
		`DBG discordrpc: activity set title=Dramaturgy large=https://tmpfiles.org/dl/art.jpg small=ongoplayer_small`,
		`DBG discordrpc: activity set title=Dramaturgy large=https://tmpfiles.org/dl/art.jpg small=ongoplayer_small repeat=+5x`,
		`INF lyrics: chain started path=ytm:dEfl-ObuapA`,
	}, "\n")
	if out != want {
		t.Fatalf("tidy output =\n  %q\nwant\n  %q", out, want)
	}
}

// TestTidyCountAfterTheLastRepeat covers the end-of-stream case: the first
// record is already printed, and Flush emits one summary line for the repeats.
func TestTidyCountAfterTheLastRepeat(t *testing.T) {
	var buf bytes.Buffer
	flush, restore := newTidyTestLogger(t, &buf)
	defer restore()

	activitySet("ongoplayer")
	activitySet("ongoplayer")
	activitySet("ongoplayer")
	flush()

	out := strings.TrimSpace(buf.String())
	want := strings.Join([]string{
		`DBG discordrpc: activity set title=Dramaturgy large=ongoplayer small=ongoplayer_small`,
		`DBG discordrpc: activity set title=Dramaturgy large=ongoplayer small=ongoplayer_small repeat=+2x`,
	}, "\n")
	if out != want {
		t.Fatalf("tidy output =\n  %q\nwant\n  %q", out, want)
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

// TestTidyKeepsFirstAndLastRepeatTimestamps proves the initial line carries the
// first timestamp immediately and the repeat summary carries the latest repeat's
// timestamp when flushed.
func TestTidyKeepsFirstAndLastRepeatTimestamps(t *testing.T) {
	first := time.Date(2026, 11, 12, 9, 3, 7, 0, time.UTC)
	last := first.Add(2 * time.Minute)

	var buf bytes.Buffer
	h := newTidyHandler(tint.NewTextHandler(&buf, &tint.Options{NoColor: true}))

	record := func(ts time.Time) slog.Record {
		r := slog.NewRecord(ts, slog.LevelDebug, "discordrpc: activity set", 0)
		r.AddAttrs(slog.String("title", "Dramaturgy"))
		return r
	}
	if err := h.Handle(context.Background(), record(first)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), first.Format(time.StampMilli)) {
		t.Fatalf("first line was not emitted immediately:\n%s", buf.String())
	}
	if err := h.Handle(context.Background(), record(last)); err != nil {
		t.Fatal(err)
	}
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), last.Format(time.StampMilli)) || !strings.Contains(buf.String(), "repeat=+1x") {
		t.Fatalf("repeat summary missing last timestamp or count:\n%s", buf.String())
	}
}
