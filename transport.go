package viewer

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Stats counts what happened to records (spec SDK-04): nothing is lost without a count.
type Stats struct {
	Logged int64 // log calls at or above the level
	Queued int64 // waiting to be sent now
	Sent   int64 // accepted by the server or the sink
	// Dropped, by reason.
	QueueFull  int64 // the oldest record made way for a newer one
	Rejected   int64 // the server refused it (partial success, or a 4xx)
	Failed     int64 // still unsent after the retries, or at Close
	AfterClose int64 // logged after Close; written to the console only
}

const (
	maxTries      = 5
	maxRetryAfter = 30 * time.Second
	requestTime   = 10 * time.Second
	defaultFlush  = 5 * time.Second
)

// exporter sends one batch. It returns how many records the receiver refused.
type exporter interface {
	export(ctx context.Context, batch []Record) (rejected int, err error)
}

// retryError marks a failure worth trying again, with the server's requested wait.
type retryError struct {
	err   error
	after time.Duration
}

func (e *retryError) Error() string { return e.err.Error() }

// pipeline is the bounded queue and the one worker that empties it (spec SDK-03).
type pipeline struct {
	mu   sync.Mutex
	buf  []Record
	head int
	n    int

	batch    int
	interval time.Duration
	exp      exporter
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error

	wake     chan struct{}
	flushes  chan flushReq
	stop     chan struct{}
	done     chan struct{}
	ctx      context.Context // cancelled when Close runs out of time
	cancel   context.CancelFunc
	sent     atomic.Int64
	full     atomic.Int64
	rejected atomic.Int64
	failed   atomic.Int64

	// Drops already reported to the server; only the worker touches these.
	reported struct {
		full, rejected, failed int64
		at                     time.Time
	}
}

type flushReq struct {
	ctx  context.Context
	done chan error
}

func newPipeline(s settings, exp exporter, now func() time.Time) *pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{
		buf: make([]Record, s.queueSize), batch: s.batchSize, interval: s.flushInterval,
		exp: exp, now: now, sleep: sleepCtx,
		wake: make(chan struct{}, 1), flushes: make(chan flushReq), stop: make(chan struct{}), done: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}
	p.reported.at = now()
	return p
}

func (p *pipeline) start() { go p.run() }

// push queues a record without waiting. A full queue drops its oldest record (SDK-04).
func (p *pipeline) push(r Record) {
	p.mu.Lock()
	if p.n == len(p.buf) {
		p.buf[p.head] = Record{}
		p.head = (p.head + 1) % len(p.buf)
		p.n--
		p.full.Add(1)
	}
	p.buf[(p.head+p.n)%len(p.buf)] = r
	p.n++
	ready := p.n >= p.batch
	p.mu.Unlock()
	if ready {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *pipeline) take(max int) []Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := min(max, p.n)
	if k == 0 {
		return nil
	}
	out := make([]Record, k)
	for i := range k {
		j := (p.head + i) % len(p.buf)
		out[i] = p.buf[j]
		p.buf[j] = Record{}
	}
	p.head = (p.head + k) % len(p.buf)
	p.n -= k
	return out
}

func (p *pipeline) queued() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int64(p.n)
}

func (p *pipeline) run() {
	defer close(p.done)
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			p.drain(p.ctx)
		case <-p.wake:
			p.drain(p.ctx)
		case req := <-p.flushes:
			ctx, cancel := mergeCancel(req.ctx, p.ctx)
			p.drain(ctx)
			err := ctx.Err() // before cancel, which would always set it
			cancel()
			if err == nil && p.queued() > 0 {
				err = errors.New("viewer: records still queued after flush")
			}
			req.done <- err
		}
	}
}

// drain sends batches until the queue is empty or ctx ends.
func (p *pipeline) drain(ctx context.Context) {
	for ctx.Err() == nil {
		batch := p.take(p.batch)
		if len(batch) == 0 {
			return
		}
		p.send(ctx, batch)
	}
}

// send delivers one batch, retrying what is worth retrying (SDK-04).
func (p *pipeline) send(ctx context.Context, batch []Record) {
	n := len(batch)
	full, rej, fail := p.full.Load(), p.rejected.Load(), p.failed.Load()
	report := full > p.reported.full || rej > p.reported.rejected || fail > p.reported.failed
	if report {
		batch = append(batch, p.dropReport(full, rej, fail))
	}
	var err error
	for try := 0; try < maxTries; try++ {
		var refused int
		refused, err = p.exp.export(ctx, batch)
		if err == nil {
			refused = min(refused, n) // the report itself isn't a user record
			p.sent.Add(int64(n - refused))
			p.rejected.Add(int64(refused))
			if report {
				p.reported.full, p.reported.rejected, p.reported.failed = full, rej, fail
				p.reported.at = p.now()
			}
			return
		}
		var re *retryError
		if !errors.As(err, &re) || try == maxTries-1 {
			break
		}
		wait := backoff(try)
		if re.after > 0 {
			wait = min(re.after, maxRetryAfter)
		}
		if p.sleep(ctx, wait) != nil {
			break
		}
	}
	var perm *permanentError
	if errors.As(err, &perm) {
		p.rejected.Add(int64(n))
	} else {
		p.failed.Add(int64(n))
	}
}

// dropReport is the warning that rides on the next batch after records were lost.
func (p *pipeline) dropReport(full, rej, fail int64) Record {
	f, r, x := full-p.reported.full, rej-p.reported.rejected, fail-p.reported.failed
	since := p.reported.at.UTC().Format(time.RFC3339)
	return Record{
		Time: p.now(), Level: LevelWarn,
		Message: fmt.Sprintf("viewer-sdk: dropped %d records since %s", f+r+x, since),
		Attrs: []slog.Attr{
			slog.Int64("viewer.dropped.queue_full", f),
			slog.Int64("viewer.dropped.rejected", r),
			slog.Int64("viewer.dropped.failed", x),
		},
	}
}

func (p *pipeline) flush(ctx context.Context) error {
	req := flushReq{ctx: ctx, done: make(chan error, 1)}
	select {
	case p.flushes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return nil
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// close flushes, then stops the worker. What is left is counted as failed.
func (p *pipeline) close(ctx context.Context) error {
	err := p.flush(ctx)
	p.cancel()
	close(p.stop)
	<-p.done
	if left := p.take(len(p.buf)); len(left) > 0 {
		p.failed.Add(int64(len(left)))
	}
	return err
}

// backoff is 0.5 s, 1 s, 2 s, 4 s … with ±20 % jitter so many clients don't retry together.
func backoff(try int) time.Duration {
	d := 500 * time.Millisecond << try
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// mergeCancel is a context that ends when either parent does.
func mergeCancel(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	stop := context.AfterFunc(b, cancel)
	return ctx, func() { stop(); cancel() }
}

// permanentError is a refusal that retrying won't fix.
type permanentError struct{ status int }

func (e *permanentError) Error() string {
	return "viewer: server refused the batch: " + strconv.Itoa(e.status)
}

// httpExporter posts OTLP/JSON with gzip (spec SDK-09).
type httpExporter struct {
	url, key string
	client   *http.Client
	enc      *encoder
	now      func() time.Time
	authOnce sync.Once
	buf      bytes.Buffer
	zbuf     bytes.Buffer
	zw       *gzip.Writer
}

func newHTTPExporter(s settings, now func() time.Time) *httpExporter {
	e := &httpExporter{url: s.endpoint, key: s.apiKey, client: &http.Client{Timeout: requestTime}, enc: newEncoder(s), now: now}
	e.zw = gzip.NewWriter(&e.zbuf)
	return e
}

// export runs on the worker goroutine only, so its buffers are reused without locking.
func (e *httpExporter) export(ctx context.Context, batch []Record) (int, error) {
	e.buf.Reset()
	e.enc.encode(&e.buf, batch, e.now())
	e.zbuf.Reset()
	e.zw.Reset(&e.zbuf)
	_, _ = e.zw.Write(e.buf.Bytes())
	_ = e.zw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(e.zbuf.Bytes()))
	if err != nil {
		return 0, &permanentError{}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("User-Agent", scopeName+"/"+Version)
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, &retryError{err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK:
		return rejectedIn(body), nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusBadGateway,
		resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusGatewayTimeout:
		return 0, &retryError{err: fmt.Errorf("viewer: server answered %d", resp.StatusCode), after: retryAfter(resp.Header.Get("Retry-After"), e.now())}
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		e.authOnce.Do(func() {
			fmt.Fprintf(stderr, "viewer: the server refused the API key (%d); check VIEWER_API_KEY\n", resp.StatusCode)
		})
	}
	return 0, &permanentError{status: resp.StatusCode}
}

// rejectedIn reads partialSuccess.rejectedLogRecords, written as a string or a number.
func rejectedIn(body []byte) int {
	var r struct {
		PartialSuccess struct {
			Rejected json.Number `json:"rejectedLogRecords"`
		} `json:"partialSuccess"`
	}
	if len(body) == 0 || json.Unmarshal(body, &r) != nil {
		return 0
	}
	s := string(r.PartialSuccess.Rejected)
	if unq, err := strconv.Unquote(s); err == nil {
		s = unq
	}
	n, _ := strconv.Atoi(s)
	return n
}

// retryAfter reads seconds or an HTTP date.
func retryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(h); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

// sinkExporter hands batches to a Sink. A sink is local, so its failures aren't retried.
type sinkExporter struct{ sink Sink }

func (e sinkExporter) export(ctx context.Context, batch []Record) (int, error) {
	if err := e.sink.Export(ctx, batch); err != nil {
		return 0, fmt.Errorf("viewer: sink: %w", err) // counted as failed, not retried
	}
	return 0, nil
}
