package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/slogtest"
	"time"
)

// TestSlogConformance runs the standard library's handler tests against the JSON console.
func TestSlogConformance(t *testing.T) {
	var out bytes.Buffer
	log := newLogger(Config{Console: ConsoleJSON, Output: &out, Level: LevelTrace}, env(nil), time.Now)
	results := func() []map[string]any {
		var ms []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var flat map[string]any
			if err := json.Unmarshal(line, &flat); err != nil {
				t.Fatal(err)
			}
			ms = append(ms, nestDotted(flat))
		}
		return ms
	}
	if err := slogtest.TestHandler(log.Handler(), results); err != nil {
		t.Fatal(err)
	}
}

// nestDotted turns the console's dotted keys back into the nested maps slogtest expects.
func nestDotted(flat map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range flat {
		if k == "service" || k == "env" {
			continue
		}
		parts := strings.Split(k, ".")
		m := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[p] = next
			}
			m = next
		}
		m[parts[len(parts)-1]] = v
	}
	return out
}

func TestSlogLevels(t *testing.T) {
	for in, want := range map[slog.Level]Level{
		slog.LevelDebug - 4: LevelTrace, slog.LevelDebug: LevelDebug, slog.LevelInfo: LevelInfo,
		slog.LevelWarn: LevelWarn, slog.LevelError: LevelError, slog.LevelError + 4: LevelFatal, slog.LevelError + 2: LevelError,
	} {
		if got := fromSlog(in); got != want {
			t.Errorf("%v → %v, want %v", in, got, want)
		}
	}
}

func TestSlogReachesTheSink(t *testing.T) {
	sink := &recordingSink{}
	log := newLogger(Config{Sink: sink, Console: ConsoleOff}, env(nil), time.Now)
	sl := slog.New(log.Handler()).With("svc", "billing").WithGroup("http")
	ctx := context.WithValue(context.Background(), scopeKey{}, &scope{attrs: []slog.Attr{slog.String("request_id", "req_A")}, traceID: [16]byte{1}})
	sl.WarnContext(ctx, "slow", "ms", 900)
	_ = log.Close(context.Background())
	r := sink.got[0]
	keys := []string{}
	for _, a := range r.Attrs {
		keys = append(keys, a.Key)
	}
	if r.Level != LevelWarn || r.Message != "slow" || strings.Join(keys, ",") != "request_id,svc,http.ms" || r.TraceID[0] != 1 {
		t.Fatalf("record %+v keys %v", r, keys)
	}
}

func TestErrorsBecomeExceptions(t *testing.T) {
	sink := &recordingSink{}
	log := newLogger(Config{Sink: sink, Console: ConsoleOff, Level: LevelTrace}, env(nil), time.Now)
	_, statErr := os.Stat("/no/such/file")
	wrapped := fmt.Errorf("loading config: %w", statErr)
	log.Error("cannot start", "err", wrapped)
	log.Warn("retrying", "err", wrapped)
	log.Info("fine", "n", 1)
	_ = log.Close(context.Background())
	get := func(r Record) map[string]string {
		m := map[string]string{}
		for _, a := range r.Attrs {
			m[a.Key] = a.Value.String()
		}
		return m
	}
	e := get(sink.got[0])
	var pe *fs.PathError
	if e["exception.type"] != fmt.Sprintf("%T", pe) || e["exception.message"] != wrapped.Error() || e["err"] != wrapped.Error() {
		t.Fatalf("error record = %v", e)
	}
	stack := e["exception.stacktrace"]
	if !strings.HasPrefix(stack, pkgPrefix+"TestErrorsBecomeExceptions\n\t") || strings.Contains(stack, pkgPrefix+"(*Logger)") {
		t.Fatalf("the stack should start at the caller:\n%s", stack)
	}
	w := get(sink.got[1])
	if _, ok := w["exception.stacktrace"]; ok || w["exception.type"] == "" {
		t.Fatalf("warn gets the type and message but no stack: %v", w)
	}
	if _, ok := get(sink.got[2])["exception.type"]; ok {
		t.Fatal("no error, no exception fields")
	}
}

func TestTextConsoleShowsTheStackUnderTheLine(t *testing.T) {
	log, out := testLogger(t, Config{}, nil)
	log.Error("boom", "err", fmt.Errorf("bad"))
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(lines[0], "exception.type=*errors.errorString") || strings.Contains(lines[0], "stacktrace") ||
		!strings.HasPrefix(lines[1], "    "+pkgPrefix+"TestTextConsoleShowsTheStackUnderTheLine") {
		t.Fatalf("got\n%s", out.String())
	}
}
