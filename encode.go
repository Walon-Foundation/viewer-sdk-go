package viewer

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Version is this SDK's version, sent as the instrumentation scope's version.
const Version = "0.1.0"

const scopeName = "viewer-sdk-go"

// encoder writes OTLP/JSON ExportLogsServiceRequest bodies by hand, following the
// opentelemetry-proto JSON mapping: camelCase names, int64 as strings, hex trace ids.
type encoder struct {
	resource []byte // the encoded resource object, the same for every batch
}

func newEncoder(s settings) *encoder {
	attrs := []slog.Attr{
		slog.String("service.name", s.service),
		slog.String("deployment.environment.name", s.env),
		slog.String("telemetry.sdk.name", scopeName),
		slog.String("telemetry.sdk.language", "go"),
		slog.String("telemetry.sdk.version", Version),
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		attrs = append(attrs, slog.String("host.name", h))
	}
	for k, v := range s.resource {
		attrs = append(attrs, slog.Any(k, v))
	}
	var b bytes.Buffer
	b.WriteString(`{"attributes":`)
	writeAttributes(&b, flatten(attrs))
	b.WriteByte('}')
	return &encoder{resource: b.Bytes()}
}

// encode writes one request holding every record of the batch.
func (e *encoder) encode(b *bytes.Buffer, records []Record, observed time.Time) {
	b.WriteString(`{"resourceLogs":[{"resource":`)
	b.Write(e.resource)
	b.WriteString(`,"scopeLogs":[{"scope":{"name":"` + scopeName + `","version":"` + Version + `"},"logRecords":[`)
	obs := strconv.FormatInt(observed.UnixNano(), 10)
	for i := range records {
		if i > 0 {
			b.WriteByte(',')
		}
		writeRecord(b, &records[i], obs)
	}
	b.WriteString(`]}]}]}`)
}

func writeRecord(b *bytes.Buffer, r *Record, observed string) {
	b.WriteString(`{"timeUnixNano":"`)
	if r.Time.IsZero() {
		b.WriteString(observed) // an unknown time is sent as when it was observed
	} else {
		b.WriteString(strconv.FormatInt(r.Time.UnixNano(), 10))
	}
	b.WriteString(`","observedTimeUnixNano":"`)
	b.WriteString(observed)
	b.WriteString(`","severityNumber":`)
	b.WriteString(strconv.Itoa(int(r.Level)))
	b.WriteString(`,"severityText":"`)
	b.WriteString(strings.ToUpper(r.Level.String()))
	b.WriteString(`","body":{"stringValue":`)
	writeJSONString(b, r.Message)
	b.WriteString(`},"attributes":`)
	writeAttributes(b, flatten(r.Attrs))
	if r.TraceID != ([16]byte{}) {
		b.WriteString(`,"traceId":"`)
		b.WriteString(hex.EncodeToString(r.TraceID[:]))
		b.WriteString(`"`)
	}
	if r.SpanID != ([8]byte{}) {
		b.WriteString(`,"spanId":"`)
		b.WriteString(hex.EncodeToString(r.SpanID[:]))
		b.WriteString(`"`)
	}
	b.WriteByte('}')
}

func writeAttributes(b *bytes.Buffer, fields []field) {
	b.WriteByte('[')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		key := f.Key
		// Durations go as milliseconds, and the key says so.
		if f.Value.Kind() == slog.KindDuration && !strings.HasSuffix(key, "_ms") {
			key += "_ms"
		}
		b.WriteString(`{"key":`)
		writeJSONString(b, key)
		b.WriteString(`,"value":`)
		writeAnyValue(b, f.Value, 0)
		b.WriteByte('}')
	}
	b.WriteByte(']')
}

const maxDepth = 5

// writeAnyValue writes an OTLP AnyValue.
func writeAnyValue(b *bytes.Buffer, v slog.Value, depth int) {
	switch v.Kind() {
	case slog.KindString:
		b.WriteString(`{"stringValue":`)
		writeJSONString(b, v.String())
		b.WriteByte('}')
	case slog.KindBool:
		b.WriteString(`{"boolValue":` + strconv.FormatBool(v.Bool()) + `}`)
	case slog.KindInt64:
		b.WriteString(`{"intValue":"` + strconv.FormatInt(v.Int64(), 10) + `"}`)
	case slog.KindUint64:
		if u := v.Uint64(); u <= math.MaxInt64 {
			b.WriteString(`{"intValue":"` + strconv.FormatUint(u, 10) + `"}`)
		} else {
			b.WriteString(`{"stringValue":"` + strconv.FormatUint(u, 10) + `"}`)
		}
	case slog.KindFloat64:
		f := v.Float64()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b.WriteString(`{"stringValue":"` + strconv.FormatFloat(f, 'g', -1, 64) + `"}`)
		} else {
			b.WriteString(`{"doubleValue":` + strconv.FormatFloat(f, 'g', -1, 64) + `}`)
		}
	case slog.KindDuration:
		b.WriteString(`{"intValue":"` + strconv.FormatInt(v.Duration().Milliseconds(), 10) + `"}`)
	case slog.KindTime:
		b.WriteString(`{"stringValue":"` + v.Time().UTC().Format(time.RFC3339Nano) + `"}`)
	case slog.KindGroup:
		writeKVList(b, flatten(v.Group()), depth)
	default:
		writeAny(b, resolve(v).Any(), depth)
	}
}

func writeAny(b *bytes.Buffer, x any, depth int) {
	switch t := x.(type) {
	case nil:
		b.WriteString(`{}`)
		return
	case error:
		b.WriteString(`{"stringValue":`)
		writeJSONString(b, t.Error())
		b.WriteByte('}')
		return
	case []byte:
		b.WriteString(`{"bytesValue":"` + base64.StdEncoding.EncodeToString(t) + `"}`)
		return
	case fmt.Stringer:
		b.WriteString(`{"stringValue":`)
		writeJSONString(b, t.String())
		b.WriteByte('}')
		return
	}
	rv := reflect.ValueOf(x)
	if depth >= maxDepth {
		b.WriteString(`{"stringValue":"[depth]"}`)
		return
	}
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			b.WriteString(`{}`)
			return
		}
		writeAny(b, rv.Elem().Interface(), depth+1)
	case reflect.Slice, reflect.Array:
		b.WriteString(`{"arrayValue":{"values":[`)
		for i := range rv.Len() {
			if i > 0 {
				b.WriteByte(',')
			}
			writeAnyValue(b, slog.AnyValue(rv.Index(i).Interface()), depth+1)
		}
		b.WriteString(`]}}`)
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			writeStringValue(b, fmt.Sprint(x))
			return
		}
		keys := rv.MapKeys()
		fields := make([]field, 0, len(keys))
		for _, k := range keys {
			fields = append(fields, field{Key: k.String(), Value: slog.AnyValue(rv.MapIndex(k).Interface())})
		}
		sortFields(fields)
		writeKVList(b, fields, depth)
	default:
		writeStringValue(b, fmt.Sprint(x))
	}
}

func writeStringValue(b *bytes.Buffer, s string) {
	b.WriteString(`{"stringValue":`)
	writeJSONString(b, s)
	b.WriteByte('}')
}

func writeKVList(b *bytes.Buffer, fields []field, depth int) {
	b.WriteString(`{"kvlistValue":{"values":[`)
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"key":`)
		writeJSONString(b, f.Key)
		b.WriteString(`,"value":`)
		writeAnyValue(b, f.Value, depth+1)
		b.WriteByte('}')
	}
	b.WriteString(`]}}`)
}
