package viewer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// stderr is where the SDK reports its own problems, once each.
var stderr io.Writer = os.Stderr

// Logger writes structured records. It is safe for concurrent use; With returns a child
// that shares the parent's console and transport.
type Logger struct {
	c     *core
	attrs []slog.Attr
	sc    *scope // set on a request's logger (FromContext)
}

// core is what a Logger and all its children share.
type core struct {
	s        settings
	console  *console
	now      func() time.Time
	pipe     *pipeline // nil when records stay on the console
	logged   atomic.Int64
	after    atomic.Int64 // logged after Close
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

// New makes a Logger. It never fails: a missing or broken setting falls back to the
// console only, and the reason is written once to stderr (spec SDK-08).
func New(cfg Config) *Logger {
	return newLogger(cfg, os.Getenv, time.Now)
}

func newLogger(cfg Config, getenv func(string) string, now func() time.Time) *Logger {
	s := resolveConfig(cfg, getenv)
	for _, p := range s.problems {
		fmt.Fprintf(stderr, "viewer: %s; logging to the console only\n", p)
	}
	c := &core{s: s, console: newConsole(s), now: now}
	switch {
	case s.sink != nil:
		c.pipe = newPipeline(s, sinkExporter{s.sink}, now)
	case s.remote():
		c.pipe = newPipeline(s, newHTTPExporter(s, now), now)
	}
	if c.pipe != nil {
		c.pipe.start()
	}
	return &Logger{c: c}
}

// With returns a logger that adds the given attributes to every record (spec SDK-02).
// Arguments are slog-style: key-value pairs or slog.Attr values.
func (l *Logger) With(args ...any) *Logger {
	attrs := argsToAttrs(args)
	if len(attrs) == 0 {
		return l
	}
	merged := make([]slog.Attr, 0, len(l.attrs)+len(attrs))
	merged = append(append(merged, l.attrs...), attrs...)
	return &Logger{c: l.c, attrs: merged, sc: l.sc}
}

// Enabled reports whether a record at level would be kept.
func (l *Logger) Enabled(level Level) bool { return level >= l.c.s.level }

// Log records msg at level with slog-style arguments.
func (l *Logger) Log(level Level, msg string, args ...any) { l.log(level, msg, args) }

func (l *Logger) Trace(msg string, args ...any) { l.log(LevelTrace, msg, args) }
func (l *Logger) Debug(msg string, args ...any) { l.log(LevelDebug, msg, args) }
func (l *Logger) Info(msg string, args ...any)  { l.log(LevelInfo, msg, args) }
func (l *Logger) Warn(msg string, args ...any)  { l.log(LevelWarn, msg, args) }
func (l *Logger) Error(msg string, args ...any) { l.log(LevelError, msg, args) }

// The Context variants add what ctx carries from a request: its request_id and trace ids.
func (l *Logger) TraceContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelTrace, msg, args)
}
func (l *Logger) DebugContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelDebug, msg, args)
}
func (l *Logger) InfoContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelInfo, msg, args)
}
func (l *Logger) WarnContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelWarn, msg, args)
}
func (l *Logger) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelError, msg, args)
}
func (l *Logger) FatalContext(ctx context.Context, msg string, args ...any) {
	l.logCtx(ctx, LevelFatal, msg, args)
}

func (l *Logger) logCtx(ctx context.Context, level Level, msg string, args []any) {
	if level < l.c.s.level {
		return
	}
	sc := scopeFrom(ctx)
	if sc == nil {
		sc = l.sc
	}
	l.logAttrs(level, l.c.now(), msg, argsToAttrs(args), sc)
}

// Fatal records msg at the fatal level. Unlike log.Fatal it does not exit: the program
// decides what happens next, and should Close the logger before it ends.
func (l *Logger) Fatal(msg string, args ...any) { l.log(LevelFatal, msg, args) }

func (l *Logger) log(level Level, msg string, args []any) {
	if level < l.c.s.level {
		return
	}
	l.logAttrs(level, l.c.now(), msg, argsToAttrs(args), l.sc)
}

// logAttrs is every log call's way in. A zero t means "unknown": the console leaves the
// time out and the record is sent with the time it was observed.
func (l *Logger) logAttrs(level Level, t time.Time, msg string, call []slog.Attr, sc *scope) {
	if level < l.c.s.level {
		return
	}
	// Logging must never take the program down (spec SDK-04).
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "viewer: a log call panicked and was dropped: %v\n", r)
		}
	}()
	attrs := l.attrs
	if sc != nil || len(call) > 0 {
		n := len(l.attrs) + len(call)
		if sc != nil {
			n += len(sc.attrs)
		}
		attrs = make([]slog.Attr, 0, n)
		if sc != nil {
			attrs = append(attrs, sc.attrs...)
		}
		attrs = append(append(attrs, l.attrs...), call...)
	}
	r := Record{Time: t, Level: level, Message: msg, Attrs: attrs}
	if sc != nil {
		r.TraceID, r.SpanID = sc.traceID, sc.spanID
	}
	l.c.emit(&r)
}

func (c *core) emit(r *Record) {
	c.logged.Add(1)
	fields := withException(flatten(r.Attrs), r.Level)
	c.console.write(r, fields)
	if c.pipe == nil {
		return
	}
	if c.closed.Load() {
		c.after.Add(1)
		return
	}
	// The queued record holds resolved values, as they were at the call.
	attrs := make([]slog.Attr, len(fields))
	for i, f := range fields {
		attrs[i] = slog.Attr{Key: f.Key, Value: f.Value}
	}
	r.Attrs = attrs
	c.pipe.push(*r)
}

// Flush sends everything queued so far. Without a deadline on ctx it waits at most five
// seconds (spec SDK-05).
func (l *Logger) Flush(ctx context.Context) error {
	if l.c.pipe == nil || l.c.closed.Load() {
		return nil
	}
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()
	return l.c.pipe.flush(ctx)
}

// Close sends what is queued and stops sending. Later log calls reach the console only.
// Calling it again returns the first call's result.
func (l *Logger) Close(ctx context.Context) error {
	l.c.once.Do(func() {
		l.c.closed.Store(true)
		if l.c.pipe == nil {
			return
		}
		ctx, cancel := withDefaultTimeout(ctx)
		defer cancel()
		l.c.closeErr = l.c.pipe.close(ctx)
	})
	return l.c.closeErr
}

// Stats reports what has happened to records so far.
func (l *Logger) Stats() Stats {
	st := Stats{Logged: l.c.logged.Load(), AfterClose: l.c.after.Load()}
	if p := l.c.pipe; p != nil {
		st.Queued = p.queued()
		st.Sent = p.sent.Load()
		st.QueueFull = p.full.Load()
		st.Rejected = p.rejected.Load()
		st.Failed = p.failed.Load()
	}
	return st
}

func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultFlush)
}
