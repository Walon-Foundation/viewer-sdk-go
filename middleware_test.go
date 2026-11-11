package viewer

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func attrMap(r Record) map[string]slog.Value {
	m := map[string]slog.Value{}
	for _, a := range r.Attrs {
		m[a.Key] = a.Value
	}
	return m
}

// serve runs one request through the middleware and returns the records it produced.
func serve(t *testing.T, h http.Handler, req *http.Request, opts ...MiddlewareOption) (*httptest.ResponseRecorder, []Record) {
	t.Helper()
	sink := &recordingSink{}
	log := newLogger(Config{Sink: sink, Console: ConsoleOff, Level: LevelTrace}, env(nil), time.Now)
	rec := httptest.NewRecorder()
	log.MiddlewareWith(opts...)(h).ServeHTTP(rec, req)
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rec, sink.got
}

func TestAccessLogLevels(t *testing.T) {
	for code, want := range map[int]Level{200: LevelInfo, 302: LevelInfo, 404: LevelWarn, 503: LevelError} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		_, got := serve(t, h, httptest.NewRequest("POST", "/invoices?x=1", nil))
		a := attrMap(got[0])
		if len(got) != 1 || got[0].Level != want || a["http.response.status_code"].Int64() != int64(code) ||
			a["http.request.method"].String() != "POST" || a["url.path"].String() != "/invoices" || got[0].Message != "POST /invoices "+a["http.response.status_code"].String() {
			t.Fatalf("%d: %+v", code, got)
		}
	}
}

func TestRequestIDAndContextLogger(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).Info("inside", "step", 1)
		slog.New(FromContext(r.Context()).Handler()).InfoContext(r.Context(), "via slog")
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "req_from_client")
	rec, got := serve(t, h, req)
	if rec.Header().Get("X-Request-ID") != "req_from_client" || len(got) != 3 {
		t.Fatalf("header %q, records %d", rec.Header().Get("X-Request-ID"), len(got))
	}
	for _, r := range got {
		if attrMap(r)["request_id"].String() != "req_from_client" {
			t.Fatalf("%q lacks the request id: %v", r.Message, r.Attrs)
		}
	}

	for _, bad := range []string{"", "has space", strings.Repeat("x", 129), "new\nline"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Request-ID", bad)
		rec, got := serve(t, h, req)
		id := rec.Header().Get("X-Request-ID")
		if len(id) != 32 || attrMap(got[2])["request_id"].String() != id {
			t.Fatalf("%q → %q", bad, id)
		}
	}
}

func TestTraceparent(t *testing.T) {
	const tid, sid = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	for h, ok := range map[string]bool{
		"00-" + tid + "-" + sid + "-01":                      true,
		"01-" + tid + "-" + sid + "-01-more":                 true, // a later version may add fields
		"ff-" + tid + "-" + sid + "-01":                      false,
		"00-" + tid + "-" + sid + "-01-more":                 false, // version 00 is exactly 55 long
		"00-" + strings.ToUpper(tid) + "-" + sid + "-01":     false,
		"00-00000000000000000000000000000000-" + sid + "-01": false,
		"00-" + tid + "-0000000000000000-01":                 false,
		"00-" + tid + "-" + sid:                              false,
		"garbage":                                            false,
	} {
		trace, span, got := parseTraceparent(h)
		if got != ok || (ok && (trace[0] != 0x4b || span[7] != 0xb7)) {
			t.Errorf("%q → %v", h, got)
		}
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { FromContext(r.Context()).Info("in trace") })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("traceparent", "00-"+tid+"-"+sid+"-01")
	_, got := serve(t, h, req)
	for _, r := range got {
		if r.TraceID[0] != 0x4b || r.SpanID[0] != 0x00 || r.SpanID[7] != 0xb7 {
			t.Fatalf("%q trace %x span %x", r.Message, r.TraceID, r.SpanID)
		}
	}
}

func TestRouteFromServeMux(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /invoices/{id}", func(w http.ResponseWriter, r *http.Request) {})
	_, got := serve(t, mux, httptest.NewRequest("GET", "/invoices/inv_9", nil))
	a := attrMap(got[0])
	if a["http.route"].String() != "/invoices/{id}" || a["url.path"].String() != "/invoices/inv_9" || got[0].Message != "GET /invoices/{id} 200" {
		t.Fatalf("attrs %v, message %q", a, got[0].Message)
	}
}

func TestSkipPaths(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	rec, got := serve(t, h, httptest.NewRequest("GET", "/healthz", nil), SkipPaths("/healthz"))
	if len(got) != 0 || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("records %v, id %q", got, rec.Header().Get("X-Request-ID"))
	}
}

func TestPanicIsLoggedAndRaised(t *testing.T) {
	sink := &recordingSink{}
	log := newLogger(Config{Sink: sink, Console: ConsoleOff}, env(nil), time.Now)
	h := log.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("handler broke") }))
	func() {
		defer func() {
			if p := recover(); p != "handler broke" {
				t.Fatalf("recovered %v", p)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	}()
	_ = log.Close(context.Background())
	a := attrMap(sink.got[0])
	if sink.got[0].Level != LevelError || a["http.response.status_code"].Int64() != 500 || a["panic"].Any() == nil || a["exception.message"].String() != "handler broke" {
		t.Fatalf("record %+v", sink.got[0])
	}
}

func TestClientAddress(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.5:51234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	_, plain := serve(t, h, req)
	_, proxied := serve(t, h, req, TrustProxy(true))
	if attrMap(plain[0])["client.address"].String() != "10.0.0.5" || attrMap(proxied[0])["client.address"].String() != "203.0.113.7" {
		t.Fatal("client address")
	}
}

func TestFlusherSurvivesTheWrapper(t *testing.T) {
	var flushed bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: 1\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController: %v", err)
		}
	})
	_, got := serve(t, h, httptest.NewRequest("GET", "/tail", nil))
	if !flushed || attrMap(got[0])["http.response.body.size"].Int64() != 9 {
		t.Fatalf("flushed %v, %v", flushed, got[0].Attrs)
	}
}

func TestFromContextOutsideARequest(t *testing.T) {
	t.Setenv("VIEWER_ENDPOINT", "https://viewer.example.org")
	t.Setenv("VIEWER_API_KEY", "vk_real")
	l := FromContext(context.Background())
	if l == nil || l.c.pipe != nil {
		t.Fatal("the fallback must be a console-only logger")
	}
	if FromContext(NewContext(context.Background(), l)) != l {
		t.Fatal("NewContext round trip")
	}
}
