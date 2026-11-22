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

// tidyHandler is a slog.Handler wrapper that collapses consecutive identical
// records without holding back the latest event. The first occurrence of a
// distinct record is emitted immediately so live console output always reflects
// current state; consecutive duplicates are counted and flushed as one summary
// line ("repeat=+Nx") when a different record arrives or Flush is called.
type tidyHandler struct {
	next slog.Handler
	on   bool

	mu         sync.Mutex
	last       *slog.Record
	suppressed int
	key        string
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

	if h.last != nil && key == h.key {
		c := r.Clone()
		h.last = &c
		h.suppressed++
		return nil
	}
	if err := h.flushLocked(); err != nil {
		return err
	}
	c := r.Clone()
	h.last = &c
	h.key = key
	h.suppressed = 0
	return h.next.Handle(ctx, r)
}

// Flush emits a trailing repeat summary if consecutive duplicates are pending.
func (h *tidyHandler) Flush() error {
	if !h.on {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.flushLocked()
}

func (h *tidyHandler) flushLocked() error {
	if h.last == nil || h.suppressed == 0 {
		return nil
	}
	r := *h.last
	n := h.suppressed
	h.suppressed = 0
	// Keep the count as a trailing attribute so the message stays greppable.
	r.AddAttrs(slog.String("repeat", fmt.Sprintf("+%dx", n)))
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
