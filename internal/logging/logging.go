// Package logging provides a slog handler that also keeps the most recent log
// lines in memory for the WebUI.
package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Ring is a bounded in-memory log buffer.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewRing creates a ring holding up to max lines.
func NewRing(max int) *Ring {
	if max <= 0 {
		max = 500
	}
	return &Ring{max: max}
}

// Append adds one line.
func (r *Ring) Append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

// Tail returns the last n lines.
func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// Handler tees records into an inner handler and a ring.
type Handler struct {
	inner slog.Handler
	ring  *Ring
	attrs []slog.Attr
}

// NewHandler wraps inner and mirrors records into ring.
func NewHandler(inner slog.Handler, ring *Ring) *Handler {
	return &Handler{inner: inner, ring: ring}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h.ring != nil {
		var b strings.Builder
		b.WriteString(r.Time.Format("15:04:05"))
		b.WriteString(" ")
		b.WriteString(r.Level.String())
		b.WriteString(" ")
		b.WriteString(r.Message)
		for _, a := range h.attrs {
			b.WriteString(" ")
			b.WriteString(a.Key)
			b.WriteString("=")
			b.WriteString(a.Value.String())
		}
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" ")
			b.WriteString(a.Key)
			b.WriteString("=")
			b.WriteString(a.Value.String())
			return true
		})
		h.ring.Append(b.String())
	}
	return h.inner.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	return &next
}
