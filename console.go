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
	"unicode/utf8"
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
	if !r.Time.IsZero() {
		b.WriteString(r.Time.Format("15:04:05.000"))
		b.WriteByte(' ')
	}
	lvl := strings.ToUpper(r.Level.String())
	b.WriteString(lvl)
	b.WriteString(strings.Repeat(" ", max(1, 6-len(lvl))))
	b.WriteString(r.Message)
	stack := ""
	for _, f := range fields {
		if f.Key == "exception.stacktrace" {
			stack = f.Value.String() // printed under the line, where it reads as a stack
			continue
		}
		b.WriteString("  ")
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(textValue(f.Value)))
	}
	if r.TraceID != ([16]byte{}) {
		fmt.Fprintf(b, "  trace_id=%x", r.TraceID)
	}
	b.WriteByte('\n')
	for _, line := range strings.Split(strings.TrimRight(stack, "\n"), "\n") {
		if line != "" {
			b.WriteString("    ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
}

func (c *console) writeJSON(r *Record, fields []field) {
	b := &c.buf
	b.WriteByte('{')
	if !r.Time.IsZero() {
		b.WriteString(`"time":`)
		writeJSONString(b, r.Time.UTC().Format(time.RFC3339Nano))
		b.WriteByte(',')
	}
	b.WriteString(`"level":`)
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

const hexDigits = "0123456789abcdef"

// writeJSONString escapes s as a JSON string without allocating; it matches
// encoding/json's output, including the HTML-safe escapes and U+2028/U+2029.
func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			b.WriteString(s[start:i])
			switch c {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(s[start:i])
			b.WriteString("\ufffd") // the replacement character itself, as encoding/json writes it
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			b.WriteString(s[start:i])
			b.WriteString(`\u202`)
			b.WriteByte(hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	b.WriteString(s[start:])
	b.WriteByte('"')
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
