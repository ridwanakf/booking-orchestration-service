package observability

import (
	"context"
	"log/slog"
)

type step struct {
	group string
	attrs []slog.Attr
}

type requestHandler struct {
	base  slog.Handler
	steps []step
}

// NewHandler stamps request_id onto records logged with a context carrying one,
// so call sites do not have to. A non-context call site gets no id.
func NewHandler(base slog.Handler) slog.Handler {
	return &requestHandler{base: base}
}

func (h *requestHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

// Groups and attrs are replayed at Handle time rather than applied eagerly, so
// request_id can be attached before the first group opens. Applying it in
// Handle would nest it inside any open group and break every query on it.
func (h *requestHandler) Handle(ctx context.Context, r slog.Record) error {
	next := h.base
	if id := RequestID(ctx); id != "" {
		next = next.WithAttrs([]slog.Attr{slog.String("request_id", id)})
	}
	for _, s := range h.steps {
		if s.group != "" {
			next = next.WithGroup(s.group)
			continue
		}
		next = next.WithAttrs(s.attrs)
	}
	return next.Handle(ctx, r)
}

func (h *requestHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(step{attrs: attrs})
}

func (h *requestHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(step{group: name})
}

func (h *requestHandler) with(s step) slog.Handler {
	steps := make([]step, len(h.steps), len(h.steps)+1)
	copy(steps, h.steps)
	return &requestHandler{base: h.base, steps: append(steps, s)}
}
