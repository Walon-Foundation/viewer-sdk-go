package viewer

import (
	"context"
	"log/slog"
)

// Handler returns a slog.Handler that writes through this logger, so existing slog code
// sends to Viewer: slog.SetDefault(slog.New(log.Handler())).
func (l *Logger) Handler() slog.Handler { return &handler{l: l} }

type handler struct {
	l      *Logger
	groups []string // open groups, outermost first
}

// slog's levels sit between ours: Debug -4, Info 0, Warn 4, Error 8.
func fromSlog(l slog.Level) Level {
	switch {
	case l >= slog.LevelError+4:
		return LevelFatal
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarn
	case l >= slog.LevelInfo:
		return LevelInfo
	case l >= slog.LevelDebug:
		return LevelDebug
	}
	return LevelTrace
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return h.l.Enabled(fromSlog(l)) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	h.l.logAttrs(fromSlog(r.Level), r.Time, r.Message, h.nest(attrs), scopeFrom(ctx))
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nested := h.nest(attrs)
	merged := make([]slog.Attr, 0, len(h.l.attrs)+len(nested))
	merged = append(append(merged, h.l.attrs...), nested...)
	return &handler{l: &Logger{c: h.l.c, attrs: merged}, groups: h.groups}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, len(h.groups), len(h.groups)+1)
	copy(groups, h.groups)
	return &handler{l: h.l, groups: append(groups, name)}
}

// nest puts attrs inside the open groups; an empty group vanishes when flattened.
func (h *handler) nest(attrs []slog.Attr) []slog.Attr {
	for i := len(h.groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: h.groups[i], Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}
