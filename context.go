package viewer

import (
	"context"
	"log/slog"
)

// scope is what a request adds to every record logged with its context: attributes such
// as request_id, and the trace it belongs to.
type scope struct {
	attrs   []slog.Attr
	traceID [16]byte
	spanID  [8]byte
}

type scopeKey struct{}

func scopeFrom(ctx context.Context) *scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey{}).(*scope)
	return s
}
