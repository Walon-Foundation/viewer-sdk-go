package viewer

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// otlp is the part of a request the tests read.
type otlp struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []kv `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			Scope struct {
				Name, Version string
			} `json:"scope"`
			LogRecords []struct {
				TimeUnixNano   string `json:"timeUnixNano"`
				SeverityNumber int    `json:"severityNumber"`
				SeverityText   string `json:"severityText"`
				Body           struct {
					StringValue string `json:"stringValue"`
				} `json:"body"`
				Attributes []kv   `json:"attributes"`
				TraceID    string `json:"traceId"`
				SpanID     string `json:"spanId"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

// server is a fake Viewer: it records each request and answers with the next response.
type server struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []otlp
	headers  []http.Header
	answers  []func(w http.ResponseWriter)
	received chan struct{}
}

func newServer(t *testing.T, answers ...func(w http.ResponseWriter)) *server {
	s := &server{t: t, answers: answers, received: make(chan struct{}, 100)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("body not gzip: %v", err)
			return
		}
		raw, _ := io.ReadAll(zr)
		var body otlp
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body not OTLP JSON: %v\n%s", err, raw)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, body)
		s.headers = append(s.headers, r.Header.Clone())
		var answer func(http.ResponseWriter)
		if len(s.answers) > 0 {
			answer, s.answers = s.answers[0], s.answers[1:]
		}
		s.mu.Unlock()
		if answer != nil {
			answer(w)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		s.received <- struct{}{}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *server) wait(n int) {
	s.t.Helper()
	for range n {
		select {
		case <-s.received:
		case <-time.After(5 * time.Second):
			s.t.Fatalf("server got fewer than %d requests", n)
		}
	}
}

func (s *server) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.reqs {
		for _, rec := range r.ResourceLogs[0].ScopeLogs[0].LogRecords {
			out = append(out, rec.Body.StringValue)
		}
	}
	return out
}

func status(code int, header ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
	}
}

func remote(t *testing.T, s *server, cfg Config) (*Logger, *[]time.Duration) {
	t.Helper()
	cfg.Endpoint, cfg.APIKey, cfg.Console = s.srv.URL, "vk_test", ConsoleOff
	if cfg.Service == "" {
		cfg.Service = "billing"
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Hour // only size or Flush sends, unless a test says otherwise
	}
	log := newLogger(cfg, env(nil), time.Now)
	var mu sync.Mutex
	waits := &[]time.Duration{}
	log.c.pipe.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	return log, waits
}

func TestSendsGzippedOTLP(t *testing.T) {
	s := newServer(t)
	log, _ := remote(t, s, Config{BatchSize: 2, Env: "prod"})
	log.With("user", "u1").Info("one", "n", 1)
	log.Error("two", "took", 1500*time.Millisecond, "ok", true, "ratio", 0.5, "tags", []string{"a"}, slog.Group("http", "status", 500))
	s.wait(1)
	h := s.headers[0]
	if h.Get("Authorization") != "Bearer vk_test" || h.Get("Content-Encoding") != "gzip" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", h)
	}
	rl := s.reqs[0].ResourceLogs[0]
	res := map[string]any{}
	for _, a := range rl.Resource.Attributes {
		res[a.Key] = a.Value["stringValue"]
	}
	if res["service.name"] != "billing" || res["deployment.environment.name"] != "prod" || res["telemetry.sdk.language"] != "go" {
		t.Fatalf("resource = %v", res)
	}
	sl := rl.ScopeLogs[0]
	if sl.Scope.Name != "viewer-sdk-go" || sl.Scope.Version != Version || len(sl.LogRecords) != 2 {
		t.Fatalf("scope = %+v", sl)
	}
	two := sl.LogRecords[1]
	attrs := map[string]map[string]any{}
	for _, a := range two.Attributes {
		attrs[a.Key] = a.Value
	}
	if two.SeverityNumber != 17 || two.SeverityText != "ERROR" || two.Body.StringValue != "two" ||
		attrs["took_ms"]["intValue"] != "1500" || attrs["ok"]["boolValue"] != true || attrs["ratio"]["doubleValue"] != 0.5 ||
		attrs["http.status"]["intValue"] != "500" || attrs["tags"]["arrayValue"] == nil {
		t.Fatalf("record = %+v / %v", two, attrs)
	}
	if one := sl.LogRecords[0]; one.Attributes[0].Key != "user" || one.Attributes[1].Value["intValue"] != "1" {
		t.Fatalf("first record attrs = %+v", one.Attributes)
	}
}

func TestBatchesByInterval(t *testing.T) {
	s := newServer(t)
	log, _ := remote(t, s, Config{FlushInterval: 30 * time.Millisecond})
	log.Info("alone")
	s.wait(1)
	if got := s.bodies(); len(got) != 1 || got[0] != "alone" {
		t.Fatalf("got %v", got)
	}
}

func TestRetriesThenSends(t *testing.T) {
	s := newServer(t, status(503), status(429, "Retry-After", "7"), status(502))
	log, waits := remote(t, s, Config{})
	log.Info("persistent")
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := log.Stats()
	if len(s.reqs) != 4 || st.Sent != 1 || st.Failed != 0 {
		t.Fatalf("requests %d, stats %+v", len(s.reqs), st)
	}
	w := *waits
	if len(w) != 3 || w[0] < 400*time.Millisecond || w[0] > 600*time.Millisecond || w[1] != 7*time.Second || w[2] < 1600*time.Millisecond {
		t.Fatalf("waits = %v (backoff, then Retry-After, then backoff)", w)
	}
}

func TestGivesUpAfterFiveTries(t *testing.T) {
	s := newServer(t, status(503), status(503), status(503), status(503), status(503))
	log, waits := remote(t, s, Config{})
	log.Info("doomed")
	_ = log.Flush(context.Background())
	if st := log.Stats(); len(s.reqs) != 5 || st.Failed != 1 || len(*waits) != 4 {
		t.Fatalf("requests %d, waits %v, stats %+v", len(s.reqs), *waits, st)
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	s := newServer(t, status(429, "Retry-After", "3600"))
	log, waits := remote(t, s, Config{})
	log.Info("x")
	_ = log.Flush(context.Background())
	if (*waits)[0] != maxRetryAfter {
		t.Fatalf("waits = %v", *waits)
	}
}

func TestRefusalsAreNotRetried(t *testing.T) {
	var warn bytes.Buffer
	old := stderr
	stderr = &warn
	t.Cleanup(func() { stderr = old })
	s := newServer(t, status(400), status(401), status(401))
	log, waits := remote(t, s, Config{BatchSize: 1})
	for _, m := range []string{"a", "b", "c"} {
		log.Info(m)
		_ = log.Flush(context.Background())
	}
	if st := log.Stats(); len(s.reqs) != 3 || st.Rejected != 3 || len(*waits) != 0 {
		t.Fatalf("requests %d, waits %v, stats %+v", len(s.reqs), *waits, st)
	}
	if strings.Count(warn.String(), "refused the API key") != 1 {
		t.Fatalf("stderr = %q (the key problem is said once)", warn.String())
	}
}

func TestPartialSuccess(t *testing.T) {
	for _, body := range []string{`{"partialSuccess":{"rejectedLogRecords":"2","errorMessage":"too big"}}`, `{"partialSuccess":{"rejectedLogRecords":2}}`} {
		s := newServer(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, body) })
		log, _ := remote(t, s, Config{})
		for range 5 {
			log.Info("r")
		}
		_ = log.Flush(context.Background())
		if st := log.Stats(); st.Sent != 3 || st.Rejected != 2 {
			t.Fatalf("%s: stats %+v", body, st)
		}
	}
}

func TestFullQueueDropsTheOldest(t *testing.T) {
	p := newPipeline(resolveConfig(Config{QueueSize: 3, BatchSize: 10}, env(nil)), nil, time.Now)
	for _, m := range []string{"1", "2", "3", "4", "5"} {
		p.push(Record{Message: m})
	}
	got := p.take(10)
	if len(got) != 3 || got[0].Message != "3" || got[2].Message != "5" || p.full.Load() != 2 {
		t.Fatalf("kept %v, dropped %d", got, p.full.Load())
	}
}

func TestDropsAreReportedOnce(t *testing.T) {
	s := newServer(t, status(400))
	log, _ := remote(t, s, Config{})
	log.Info("refused")
	_ = log.Flush(context.Background()) // rejected: one drop to report
	log.Info("next")
	_ = log.Flush(context.Background())
	log.Info("after")
	_ = log.Flush(context.Background())
	got := s.bodies()
	if len(got) != 4 || got[1] != "next" || !strings.HasPrefix(got[2], "viewer-sdk: dropped 1 records since ") || got[3] != "after" {
		t.Fatalf("bodies = %q", got)
	}
	rec := s.reqs[1].ResourceLogs[0].ScopeLogs[0].LogRecords[1]
	if rec.SeverityNumber != int(LevelWarn) || rec.Attributes[1].Key != "viewer.dropped.rejected" || rec.Attributes[1].Value["intValue"] != "1" {
		t.Fatalf("report = %+v", rec)
	}
	if st := log.Stats(); st.Sent != 2 || st.Rejected != 1 {
		t.Fatalf("the report isn't counted as a user record: %+v", st)
	}
}

func TestCloseSendsEverythingAndStops(t *testing.T) {
	s := newServer(t)
	log, _ := remote(t, s, Config{BatchSize: 1000})
	for range 1200 {
		log.Info("r")
	}
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	log.Info("late")
	if err := log.Close(context.Background()); err != nil {
		t.Fatal("second Close:", err)
	}
	if st := log.Stats(); st.Sent != 1200 || st.AfterClose != 1 || st.Queued != 0 || len(s.reqs) != 2 {
		t.Fatalf("stats %+v, requests %d", st, len(s.reqs))
	}
}

func TestCloseGivesUpAtItsDeadline(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(hang); srv.Close() })
	log := newLogger(Config{Endpoint: srv.URL, APIKey: "k", Console: ConsoleOff, FlushInterval: time.Hour}, env(nil), time.Now)
	log.Info("stuck")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := log.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if st := log.Stats(); st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestNeverBlocks: with the server hanging, logging stays fast and memory stays at the
// queue's size (spec SDK-03, SDK-04).
func TestNeverBlocks(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(hang); srv.Close() })
	log := newLogger(Config{Endpoint: srv.URL, APIKey: "k", Console: ConsoleOff, QueueSize: 1000, BatchSize: 100}, env(nil), time.Now)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = log.Close(ctx)
	})
	var wg sync.WaitGroup
	var slowest atomic.Int64
	start := time.Now()
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				t0 := time.Now()
				log.Info("busy", "i", i)
				if d := int64(time.Since(t0)); d > slowest.Load() {
					slowest.Store(d)
				}
			}
		}()
	}
	wg.Wait()
	st := log.Stats()
	if st.Queued > 1000 || st.QueueFull < 90_000 || time.Since(start) > 10*time.Second {
		t.Fatalf("stats %+v after %v", st, time.Since(start))
	}
	t.Logf("100 000 calls in %v, slowest %v", time.Since(start), time.Duration(slowest.Load()))
}

type recordingSink struct {
	mu   sync.Mutex
	got  []Record
	fail bool
}

func (s *recordingSink) Export(_ context.Context, rs []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("database down")
	}
	s.got = append(s.got, rs...)
	return nil
}

func TestSinkReplacesHTTP(t *testing.T) {
	sink := &recordingSink{}
	log := newLogger(Config{Sink: sink, Console: ConsoleOff}, env(nil), time.Now)
	log.With("a", 1).Info("to the sink", slog.Group("g", "b", 2))
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 || sink.got[0].Message != "to the sink" || sink.got[0].Attrs[1].Key != "g.b" {
		t.Fatalf("sink got %+v", sink.got)
	}
	failing := &recordingSink{fail: true}
	log = newLogger(Config{Sink: failing, Console: ConsoleOff}, env(nil), time.Now)
	log.Info("lost")
	_ = log.Close(context.Background())
	if st := log.Stats(); st.Failed != 1 {
		t.Fatalf("a failing sink counts as failed, not retried: %+v", st)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if retryAfter("12", now) != 12*time.Second || retryAfter("", now) != 0 || retryAfter("soon", now) != 0 ||
		retryAfter("Wed, 11 Nov 2026 12:00:30 GMT", now) != 30*time.Second {
		t.Fatal("Retry-After parsing")
	}
}
