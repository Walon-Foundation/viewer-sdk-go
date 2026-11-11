package viewer

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

var fixed = time.Date(2026, 11, 11, 10, 58, 41, 207_000_000, time.UTC)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// testLogger logs to a buffer at a fixed time with the given console mode.
func testLogger(t *testing.T, cfg Config, vars map[string]string) (*Logger, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	cfg.Output = &out
	return newLogger(cfg, env(vars), func() time.Time { return fixed }), &out
}

func TestLevels(t *testing.T) {
	for _, c := range []struct {
		l    Level
		name string
	}{{LevelTrace, "trace"}, {LevelDebug, "debug"}, {LevelInfo, "info"}, {LevelWarn, "warn"}, {LevelError, "error"}, {LevelFatal, "fatal"}} {
		if c.l.String() != c.name {
			t.Errorf("%d = %q", c.l, c.l.String())
		}
		got, err := ParseLevel(strings.ToUpper(c.name))
		if err != nil || got != c.l {
			t.Errorf("ParseLevel(%q) = %v, %v", c.name, got, err)
		}
	}
	if l, _ := ParseLevel("warning"); l != LevelWarn {
		t.Error("warning should be warn")
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("unknown level accepted")
	}
	if []int{1, 5, 9, 13, 17, 21}[3] != int(LevelWarn) {
		t.Error("severity numbers must match OTel (SDK-01)")
	}
}

func TestConfigDefaultsAndEnv(t *testing.T) {
	s := resolveConfig(Config{}, env(nil))
	if s.env != "development" || s.level != LevelInfo || s.queueSize != 10_000 || s.batchSize != 500 ||
		s.flushInterval != time.Second || s.console != ConsoleText || s.remote() || len(s.problems) != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	s = resolveConfig(Config{Service: "billing"}, env(map[string]string{
		"VIEWER_ENDPOINT": "https://viewer.example.org", "VIEWER_API_KEY": "vk_1",
		"VIEWER_SERVICE": "ignored", "VIEWER_ENV": "production", "VIEWER_LEVEL": "debug",
	}))
	if s.endpoint != "https://viewer.example.org/v1/logs" || s.apiKey != "vk_1" || s.service != "billing" ||
		s.env != "production" || s.level != LevelDebug || s.console != ConsoleJSON || !s.remote() {
		t.Fatalf("from env = %+v", s)
	}
}

func TestEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"https://v.example.org":         "https://v.example.org/v1/logs",
		"https://v.example.org/":        "https://v.example.org/v1/logs",
		"https://v.example.org/v1/logs": "https://v.example.org/v1/logs",
		"http://localhost:8300/viewer/": "http://localhost:8300/viewer/v1/logs",
	} {
		if got, err := normalizeEndpoint(in); err != nil || got != want {
			t.Errorf("%q → %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"viewer.example.org", "ftp://x/y", "https://"} {
		if _, err := normalizeEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHalfAConfigIsReported(t *testing.T) {
	var warn bytes.Buffer
	old := stderr
	stderr = &warn
	t.Cleanup(func() { stderr = old })
	testLogger(t, Config{}, map[string]string{"VIEWER_ENDPOINT": "https://v.example.org"})
	testLogger(t, Config{}, map[string]string{"VIEWER_API_KEY": "k"})
	testLogger(t, Config{}, map[string]string{"VIEWER_LEVEL": "shouty"})
	testLogger(t, Config{}, nil) // nothing set: local development, nothing to say
	got := warn.String()
	if strings.Count(got, "\n") != 3 || !strings.Contains(got, "no API key") || !strings.Contains(got, "no endpoint") || !strings.Contains(got, "VIEWER_LEVEL") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestArgs(t *testing.T) {
	got := argsToAttrs([]any{"a", 1, slog.String("b", "x"), "c", true, 42, "dangling"})
	want := []slog.Attr{slog.Int("a", 1), slog.String("b", "x"), slog.Bool("c", true), slog.Int(badKey, 42), slog.String(badKey, "dangling")}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("%d: %v, want %v", i, got[i], want[i])
		}
	}
}

func TestTextConsole(t *testing.T) {
	log, out := testLogger(t, Config{Service: "billing"}, nil)
	log.Info("invoice created", "invoice_id", "inv_1", "amount", 4200, "note", "two words", slog.Group("http", "status", 201))
	log.Debug("not shown")
	want := `10:58:41.207 INFO  invoice created  invoice_id=inv_1  amount=4200  note="two words"  http.status=201` + "\n"
	if out.String() != want {
		t.Fatalf("got  %q\nwant %q", out.String(), want)
	}
}

func TestJSONConsole(t *testing.T) {
	log, out := testLogger(t, Config{Service: "billing", Env: "prod"}, nil)
	log.With("user", "u1").Error("payment failed", "err", errors.New("card declined"),
		"took", 1500*time.Millisecond, "ratio", math.NaN(), "tags", []string{"a", "b"})
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if got["time"] != "2026-11-11T10:58:41.207Z" || got["level"] != "error" || got["service"] != "billing" ||
		got["env"] != "prod" || got["msg"] != "payment failed" || got["user"] != "u1" ||
		got["err"] != "card declined" || got["took"] != 1500.0 || got["ratio"] != "NaN" || len(got["tags"].([]any)) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestWithLeavesTheParentAlone(t *testing.T) {
	log, out := testLogger(t, Config{}, nil)
	a := log.With("k", "a")
	b := a.With("k2", "b")
	_ = a.With("k3", "c") // must not reach b's attributes through a shared backing array
	b.Info("x")
	a.Info("y")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasSuffix(lines[0], "x  k=a  k2=b") || !strings.HasSuffix(lines[1], "y  k=a") {
		t.Fatalf("lines = %q", lines)
	}
}

type boom struct{}

func (boom) LogValue() slog.Value { panic("kaboom") }

func TestNeverPanics(t *testing.T) {
	log, out := testLogger(t, Config{}, nil)
	log.Warn("still here", "v", boom{})
	if !strings.Contains(out.String(), `v="!PANIC: kaboom"`) {
		t.Fatalf("got %q", out.String())
	}
}

func TestFatalDoesNotExit(t *testing.T) {
	log, out := testLogger(t, Config{}, nil)
	log.Fatal("disk gone")
	if !strings.Contains(out.String(), "FATAL disk gone") {
		t.Fatalf("got %q", out.String())
	}
	// Reaching this line is the test: the process is still running.
}

func TestConsoleOffAndLevel(t *testing.T) {
	log, out := testLogger(t, Config{Console: ConsoleOff}, nil)
	log.Error("hidden")
	if out.Len() != 0 {
		t.Fatal("console off still wrote")
	}
	log, _ = testLogger(t, Config{Level: LevelWarn}, nil)
	if log.Enabled(LevelInfo) || !log.Enabled(LevelWarn) {
		t.Fatal("Enabled ignores the level")
	}
}

func TestJSONStringMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{"", "plain", `quote " and \ slash`, "line\nbreak\ttab\r", "\x00\x1f", "<a href='x'>&", "héllo wörld ✓", "bad \xff byte", "  ", "emoji 🚀"} {
		var b bytes.Buffer
		writeJSONString(&b, s)
		want, _ := json.Marshal(s)
		if b.String() != string(want) {
			t.Errorf("%q: got %s, want %s", s, b.String(), want)
		}
	}
}
