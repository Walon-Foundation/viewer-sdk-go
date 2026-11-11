package viewer

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// scope is what a request adds to every record logged with its context: attributes such
// as request_id, and the trace it belongs to.
type scope struct {
	attrs   []slog.Attr
	traceID [16]byte
	spanID  [8]byte
}

type (
	scopeKey  struct{}
	loggerKey struct{}
)

func scopeFrom(ctx context.Context) *scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey{}).(*scope)
	return s
}

// NewContext returns a context carrying l, for FromContext.
func NewContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromContext returns the logger the middleware put in a request's context: it adds the
// request's request_id and trace ids to every record. Outside a request, or for a context
// without one, it returns a console-only logger, so the call is always safe.
func FromContext(ctx context.Context) *Logger {
	if ctx != nil {
		if l, ok := ctx.Value(loggerKey{}).(*Logger); ok && l != nil {
			return l
		}
	}
	return fallback()
}

// fallback is console only even when VIEWER_ENDPOINT and VIEWER_API_KEY are set: a
// logger nobody will Close must not start sending.
var fallback = sync.OnceValue(func() *Logger {
	getenv := func(k string) string {
		if k == "VIEWER_ENDPOINT" || k == "VIEWER_API_KEY" {
			return ""
		}
		return os.Getenv(k)
	}
	return newLogger(Config{}, getenv, time.Now)
})
