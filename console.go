package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// console writes records to stdout (spec D-07, SDK-06): readable text in development,
// one JSON object per line in production.
type console struct {
	mu      sync.Mutex
	w       io.Writer
	json    bool
	service string
	env     string
	buf     bytes.Buffer
}

func newConsole(s settings) *console {
	if s.console == ConsoleOff {
		return nil
	}
	return &console{w: s.output, json: s.console == ConsoleJSON, service: s.service, env: s.env}
}

func (c *console) write(r *Record, fields []field) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Reset()
	if c.json {
		c.writeJSON(r, fields)
	} else {
		c.writeText(r, fields)
	}
	_, _ = c.w.Write(c.buf.Bytes())
}

func (c *console) writeText(r *Record, fields []field) {
	b := &c.buf
	b.WriteString(r.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	lvl := strings.ToUpper(r.Level.String())
	b.WriteString(lvl)
	b.WriteString(strings.Repeat(" ", max(1, 6-len(lvl))))
	b.WriteString(r.Message)
	for _, f := range fields {
		b.WriteString("  ")
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(textValue(f.Value)))
	}
	if r.TraceID != ([16]byte{}) {
		fmt.Fprintf(b, "  trace_id=%x", r.TraceID)
	}
	b.WriteByte('\n')
}

func (c *console) writeJSON(r *Record, fields []field) {
	b := &c.buf
	b.WriteString(`{"time":`)
	writeJSONString(b, r.Time.UTC().Format(time.RFC3339Nano))
	b.WriteString(`,"level":`)
	writeJSONString(b, r.Level.String())
	b.WriteString(`,"service":`)
	writeJSONString(b, c.service)
	b.WriteString(`,"env":`)
	writeJSONString(b, c.env)
	b.WriteString(`,"msg":`)
	writeJSONString(b, r.Message)
	if r.TraceID != ([16]byte{}) {
		fmt.Fprintf(b, `,"trace_id":"%x","span_id":"%x"`, r.TraceID, r.SpanID)
	}
	for _, f := range fields {
		b.WriteByte(',')
		writeJSONString(b, f.Key)
		b.WriteByte(':')
		writeJSONValue(b, f.Value)
	}
	b.WriteString("}\n")
}

// textValue is a value as the text console shows it.
func textValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return x.Error()
		case fmt.Stringer:
			return x.String()
		case []byte:
			return string(x)
		}
	}
	return fmt.Sprint(v.Any())
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r == '"' || r == '=' || !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

func writeJSONString(b *bytes.Buffer, s string) {
	out, _ := json.Marshal(s)
	b.Write(out)
}

// writeJSONValue writes a value as JSON; anything json can't encode is written as a string.
func writeJSONValue(b *bytes.Buffer, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		writeJSONString(b, v.String())
		return
	case slog.KindInt64:
		b.WriteString(strconv.FormatInt(v.Int64(), 10))
		return
	case slog.KindUint64:
		b.WriteString(strconv.FormatUint(v.Uint64(), 10))
		return
	case slog.KindFloat64:
		f := v.Float64()
		if f != f || f > 1e308 || f < -1e308 { // NaN and ±Inf aren't JSON
			writeJSONString(b, strconv.FormatFloat(f, 'g', -1, 64))
			return
		}
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		return
	case slog.KindBool:
		b.WriteString(strconv.FormatBool(v.Bool()))
		return
	case slog.KindDuration:
		b.WriteString(strconv.FormatInt(v.Duration().Milliseconds(), 10))
		return
	case slog.KindTime:
		writeJSONString(b, v.Time().UTC().Format(time.RFC3339Nano))
		return
	}
	x := v.Any()
	if err, ok := x.(error); ok {
		writeJSONString(b, err.Error())
		return
	}
	out, err := json.Marshal(x)
	if err != nil {
		writeJSONString(b, fmt.Sprint(x))
		return
	}
	b.Write(out)
}
