package viewer

import (
	"log/slog"
	"time"
)

// Record is one log call, as it is queued and exported.
type Record struct {
	Time    time.Time
	Level   Level
	Message string
	// Attrs are the logger's bound attributes followed by the call's own.
	Attrs []slog.Attr
	// TraceID and SpanID are zero when the record is outside a trace.
	TraceID [16]byte
	SpanID  [8]byte
}

const badKey = "!BADKEY"

// argsToAttrs reads slog-style arguments: key-value pairs, slog.Attr values, or a lone
// value, which gets the key "!BADKEY" as in log/slog.
func argsToAttrs(args []any) []slog.Attr {
	if len(args) == 0 {
		return nil
	}
	out := make([]slog.Attr, 0, (len(args)+1)/2)
	for len(args) > 0 {
		switch k := args[0].(type) {
		case slog.Attr:
			out = append(out, k)
			args = args[1:]
		case string:
			if len(args) == 1 {
				out = append(out, slog.String(badKey, k))
				return out
			}
			out = append(out, slog.Any(k, args[1]))
			args = args[2:]
		default:
			out = append(out, slog.Any(badKey, k))
			args = args[1:]
		}
	}
	return out
}
