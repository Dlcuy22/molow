package backend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// debugTidy reports whether consecutive identical log lines should collapse
// into one line with a repeat count. Tidy is on unless MOLOW_DEBUG_TIDY is set
// to 0 or false.
func debugTidy() bool {
	switch os.Getenv("MOLOW_DEBUG_TIDY") {
	case "0", "false":
		return false
	}
	return true
}

// tidyHandler is a slog.Handler wrapper that collapses consecutive records
// sharing a level, message and attributes into one line with a trailing
// repeat=Nx attribute. Sources that re-emit the same activity every few
// seconds, such as the Discord presence, then read as one line plus a count
// instead of a stream.
//
// A record is held back until the next, different record arrives, because the
// count is only known after the repeats happen. Handle may be called from
// several goroutines, so the state sits behind a mutex.
type tidyHandler struct {
	next slog.Handler
	on   bool

	mu      sync.Mutex
	pending *slog.Record
	count   int
	key     string
}

// newTidyHandler wraps next. The wrapper is a no-op when MOLOW_DEBUG_TIDY
// turns the collapsing off.
func newTidyHandler(next slog.Handler) *tidyHandler {
	return &tidyHandler{next: next, on: debugTidy()}
}

func (h *tidyHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *tidyHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.on {
		return h.next.Handle(ctx, r)
	}

	key := recordKey(r)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pending != nil && key == h.key {
		h.count++
		return nil
	}
	if err := h.flushLocked(); err != nil {
		return err
	}
	c := r.Clone()
	h.pending = &c
	h.key = key
	h.count = 1
	return nil
}

// Flush sends the held record on, so a stream ending in repeats still shows
// them. The count joins the message when the record repeated.
func (h *tidyHandler) Flush() error {
	if !h.on {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.flushLocked()
}

func (h *tidyHandler) flushLocked() error {
	if h.pending == nil {
		return nil
	}
	r := *h.pending
	h.pending = nil
	if h.count > 1 {
		// The count is a trailing attribute rather than message text, so the
		// message stays greppable and the line still ends with the number.
		r.AddAttrs(slog.String("repeat", fmt.Sprintf("%dx", h.count)))
	}
	return h.next.Handle(context.Background(), r)
}

func (h *tidyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &tidyHandler{next: h.next.WithAttrs(attrs), on: h.on}
}

func (h *tidyHandler) WithGroup(name string) slog.Handler {
	return &tidyHandler{next: h.next.WithGroup(name), on: h.on}
}

// recordKey identifies a record by its level, message and attributes in
// order. The call site is deliberately left out: the recorded PC is not
// stable between calls from the same line under -race, so identical records
// from one source would never merge.
func recordKey(r slog.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %q ", r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, "%s=%v ", a.Key, a.Value)
		return true
	})
	return b.String()
}
