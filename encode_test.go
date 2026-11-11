package viewer

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/record.golden.json")

// TestRecordGolden locks the OTLP/JSON of one record with every kind of value. The TS SDK
// (plan 7) encodes the same fixture to the same JSON.
func TestRecordGolden(t *testing.T) {
	r := Record{
		Time:    time.Date(2026, 11, 11, 10, 58, 41, 207_000_001, time.UTC),
		Level:   LevelError,
		Message: "payment failed: card declined",
		TraceID: [16]byte{0x5b, 0x8e, 0xff, 0xf7, 0x98, 0x03, 0x81, 0x03, 0xd2, 0x69, 0xb6, 0x33, 0x81, 0x3f, 0xc6, 0x0c},
		SpanID:  [8]byte{0xee, 0xe1, 0x9b, 0x7e, 0xc3, 0xc1, 0xb1, 0x74},
		Attrs: []slog.Attr{
			slog.String("request_id", "req_A"),
			slog.Int("http.status_code", 500),
			slog.Uint64("big", math.MaxUint64),
			slog.Float64("ratio", 0.25),
			slog.Float64("nan", math.NaN()),
			slog.Bool("retry", false),
			slog.Duration("took", 1500*time.Millisecond),
			slog.Time("at", time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)),
			slog.Any("err", errors.New("card declined")),
			slog.Any("raw", []byte("hi")),
			slog.Any("tags", []any{"a", 2, true}),
			slog.Any("meta", map[string]any{"z": 1, "a": "x"}),
			slog.Group("http", slog.String("route", "POST /invoices")),
			slog.Any("nothing", nil),
		},
	}
	var b bytes.Buffer
	writeRecord(&b, &r, "1794394722000000000")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b.Bytes(), "", "  "); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b.String())
	}
	pretty.WriteByte('\n')
	const path = "testdata/record.golden.json"
	if *update {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Fatalf("encoding changed; run go test -run RecordGolden -update if that's intended\n%s", pretty.String())
	}
}
