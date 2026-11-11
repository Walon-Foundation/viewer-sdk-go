package viewer

import (
	"fmt"
	"io"
	"log/slog"
	"os"
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
}

// core is what a Logger and all its children share.
type core struct {
	s       settings
	console *console
	now     func() time.Time
	stats   counters
}

type counters struct {
	logged atomic.Int64
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
	return &Logger{c: &core{s: s, console: newConsole(s), now: now}}
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
	return &Logger{c: l.c, attrs: merged}
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

// Fatal records msg at the fatal level. Unlike log.Fatal it does not exit: the program
// decides what happens next, and should Close the logger before it ends.
func (l *Logger) Fatal(msg string, args ...any) { l.log(LevelFatal, msg, args) }

func (l *Logger) log(level Level, msg string, args []any) {
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
	if call := argsToAttrs(args); len(call) > 0 {
		attrs = make([]slog.Attr, 0, len(l.attrs)+len(call))
		attrs = append(append(attrs, l.attrs...), call...)
	}
	r := Record{Time: l.c.now(), Level: level, Message: msg, Attrs: attrs}
	l.c.emit(&r)
}

func (c *core) emit(r *Record) {
	c.stats.logged.Add(1)
	c.console.write(r, flatten(r.Attrs))
}
