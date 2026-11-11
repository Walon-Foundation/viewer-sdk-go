package viewer

import (
	"fmt"
	"log/slog"
)

// field is one attribute after groups are flattened into dotted keys and LogValuers are
// resolved: what the console and the encoder both write.
type field struct {
	Key   string
	Value slog.Value
}

// flatten turns attributes into fields. Groups become dotted keys ("http.status"), empty
// groups and empty attributes vanish, and a LogValuer that panics becomes "!PANIC: …".
func flatten(attrs []slog.Attr) []field {
	out := make([]field, 0, len(attrs))
	for _, a := range attrs {
		out = appendField(out, "", a)
	}
	return out
}

func appendField(out []field, prefix string, a slog.Attr) []field {
	v := resolve(a.Value)
	if a.Equal(slog.Attr{}) || (a.Key == "" && v.Kind() != slog.KindGroup) {
		return out
	}
	key := a.Key
	if prefix != "" && key != "" {
		key = prefix + "." + key
	} else if prefix != "" {
		key = prefix
	}
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			out = appendField(out, key, g)
		}
		return out
	}
	return append(out, field{Key: key, Value: v})
}

// resolve follows LogValuers like slog.Value.Resolve, but a panic becomes a short
// "!PANIC: …" value instead of slog's multi-line stack.
func resolve(v slog.Value) (out slog.Value) {
	defer func() {
		if r := recover(); r != nil {
			out = slog.StringValue(fmt.Sprintf("!PANIC: %v", r))
		}
	}()
	for range 100 { // slog's limit for LogValuers that return LogValuers
		if v.Kind() != slog.KindLogValuer {
			return v
		}
		v = v.LogValuer().LogValue()
	}
	return slog.StringValue("!ERROR: LogValue loop")
}
