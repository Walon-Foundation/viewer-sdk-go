package viewer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

// MiddlewareOption changes what the middleware does.
type MiddlewareOption func(*middleware)

// SkipPaths names paths, such as /healthz, that get no access log. They still get a
// request id and a request-scoped logger.
func SkipPaths(paths ...string) MiddlewareOption {
	return func(m *middleware) {
		for _, p := range paths {
			m.skip[p] = true
		}
	}
}

// TrustProxy reads the client address from X-Forwarded-For. Only turn it on behind a proxy
// that sets that header: otherwise any client can write its own address.
func TrustProxy(on bool) MiddlewareOption { return func(m *middleware) { m.trustProxy = on } }

type middleware struct {
	l          *Logger
	next       http.Handler
	skip       map[string]bool
	trustProxy bool
}

// Middleware wraps next with request logging (spec SDK-10). Its shape is the standard
// func(http.Handler) http.Handler, so it works with net/http, chi, gorilla, and with echo
// and gin through their standard-handler adapters.
func (l *Logger) Middleware(next http.Handler) http.Handler { return l.MiddlewareWith()(next) }

// MiddlewareWith is Middleware with options.
func (l *Logger) MiddlewareWith(opts ...MiddlewareOption) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		m := &middleware{l: l, next: next, skip: map[string]bool{}}
		for _, o := range opts {
			o(m)
		}
		return m
	}
}

func (m *middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := m.l.c.now()
	id := r.Header.Get("X-Request-ID")
	if !validRequestID(id) {
		id = newRequestID()
	}
	w.Header().Set("X-Request-ID", id)

	sc := &scope{attrs: []slog.Attr{slog.String("request_id", id)}}
	sc.traceID, sc.spanID, _ = parseTraceparent(r.Header.Get("traceparent"))
	reqLog := &Logger{c: m.l.c, attrs: m.l.attrs, sc: sc}
	ctx := context.WithValue(r.Context(), scopeKey{}, sc)
	ctx = context.WithValue(ctx, loggerKey{}, reqLog)
	r = r.WithContext(ctx)

	rw := &responseWriter{ResponseWriter: w}
	defer func() {
		p := recover()
		if !m.skip[r.URL.Path] {
			m.access(reqLog, r, rw, start, p)
		}
		if p != nil {
			panic(p) // the server's own recovery still runs
		}
	}()
	m.next.ServeHTTP(rw, r)
}

// access writes the request's one log: error for 5xx, warn for 4xx, info otherwise.
func (m *middleware) access(l *Logger, r *http.Request, rw *responseWriter, start time.Time, p any) {
	status := rw.status
	if status == 0 {
		status = http.StatusOK
		if p != nil {
			status = http.StatusInternalServerError
		}
	}
	level := LevelInfo
	switch {
	case status >= 500:
		level = LevelError
	case status >= 400:
		level = LevelWarn
	}
	route := r.Pattern // set by http.ServeMux (Go 1.23+) on the request it routed
	if i := strings.IndexByte(route, ' '); i >= 0 {
		route = route[i+1:] // "GET /invoices/{id}" → "/invoices/{id}"
	}
	where := slog.String("url.path", r.URL.Path)
	if route != "" {
		where = slog.String("http.route", route)
	}
	ms := float64(m.l.c.now().Sub(start).Microseconds()) / 1000
	attrs := []slog.Attr{
		slog.String("http.request.method", r.Method),
		where,
		slog.Int("http.response.status_code", status),
		slog.Float64("duration_ms", math.Round(ms*1000)/1000),
		slog.Int64("http.response.body.size", rw.bytes),
		slog.String("client.address", m.clientAddress(r)),
		slog.String("user_agent.original", r.UserAgent()),
	}
	if route != "" {
		attrs = append(attrs, slog.String("url.path", r.URL.Path))
	}
	if p != nil {
		attrs = append(attrs, slog.Any("panic", fmt.Errorf("%v", p)))
	}
	target := route
	if target == "" {
		target = r.URL.Path
	}
	l.logAttrs(level, m.l.c.now(), fmt.Sprintf("%s %s %d", r.Method, target, status), attrs, l.sc)
}

func (m *middleware) clientAddress(r *http.Request) string {
	if m.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if a := strings.TrimSpace(first); a != "" {
				return a
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// validRequestID accepts an incoming id that is short and plain, so a client can't stuff
// a log line with it.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// parseTraceparent reads a W3C traceparent: version-traceid-spanid-flags.
func parseTraceparent(h string) (trace [16]byte, span [8]byte, ok bool) {
	h = strings.TrimSpace(h)
	if len(h) < 55 || h[2] != '-' || h[35] != '-' || h[52] != '-' {
		return
	}
	version := h[0:2]
	if version == "ff" || !isHex(version) || (version == "00" && len(h) != 55) || (len(h) > 55 && h[55] != '-') {
		return
	}
	if _, err := hex.Decode(trace[:], []byte(h[3:35])); err != nil || !isLowerHex(h[3:35]) {
		return [16]byte{}, [8]byte{}, false
	}
	if _, err := hex.Decode(span[:], []byte(h[36:52])); err != nil || !isLowerHex(h[36:52]) {
		return [16]byte{}, [8]byte{}, false
	}
	if !isHex(h[53:55]) || trace == ([16]byte{}) || span == ([8]byte{}) {
		return [16]byte{}, [8]byte{}, false
	}
	return trace, span, true
}

func isHex(s string) bool { return isLowerHex(strings.ToLower(s)) }

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// responseWriter notes the status and size, and keeps the optional interfaces of the
// writer it wraps working (the live tail needs Flush).
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *responseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		f.Flush()
	}
}

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("viewer: the response writer can't be hijacked")
}

func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := io.Copy(w.ResponseWriter, r)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the original writer.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
