# viewer-sdk-go

Go logger SDK for the Viewer logging platform: structured logging to the console and to
Viewer over OTLP/HTTP, a `slog.Handler`, and `net/http` middleware.

```bash
go get github.com/Walon-Foundation/viewer-sdk-go@latest
```

```go
import viewer "github.com/Walon-Foundation/viewer-sdk-go"

log := viewer.New(viewer.Config{Service: "billing", Env: "prod"}) // the rest from env
defer log.Close(ctx)

log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200)
log.With("user_id", uid).Error("payment failed", "err", err)

slog.SetDefault(slog.New(log.Handler()))          // existing slog code sends too
http.ListenAndServe(":8080", log.Middleware(mux)) // one access log per request
viewer.FromContext(r.Context()).Info("inside")    // carries request_id and trace ids
```

## Requests

`log.Middleware` is a plain `func(http.Handler) http.Handler`, so it works with
`net/http`, chi, gorilla, and echo or gin through their standard-handler adapters.
`log.MiddlewareWith(viewer.SkipPaths("/healthz"), viewer.TrustProxy(true))` takes options.

For each request it:
- reads `X-Request-ID` or makes one, and echoes it on the response;
- reads a W3C `traceparent` header for the trace and span ids;
- puts a request-scoped logger in the context, for `viewer.FromContext`, and the
  `…Context` methods and the slog handler pick the request up from the context too;
- writes one access log when the handler returns: method, route (from `http.ServeMux`
  patterns) or path, status, `duration_ms`, body size, client address, user agent.
  5xx logs at `error`, 4xx at `warn`, everything else at `info`. A panic is logged as a
  500 and raised again.

`TrustProxy` reads the client address from `X-Forwarded-For`; turn it on only behind a
proxy that sets it.

## Errors

An `error` value under any key adds `exception.type` and `exception.message`, and for
`error` and `fatal` records `exception.stacktrace`, starting at your code.

## Sending

Records go into a bounded queue (10 000) and are sent in batches of up to 500, or every
second, as OTLP/HTTP JSON with gzip. Failures are retried with backoff (at most five
tries, honouring `Retry-After`). When the queue is full the oldest record is dropped.
Nothing is lost silently: `log.Stats()` counts every drop, and the next batch that gets
through carries a warning saying how many were dropped.

`log.Flush(ctx)` sends what is queued; `log.Close(ctx)` does that and stops. Both wait at
most five seconds when `ctx` has no deadline.

`Config.Sink` replaces HTTP with your own `Export(ctx, []viewer.Record) error`.

## Settings

| Config field | Environment | Default |
|---|---|---|
| `Endpoint` | `VIEWER_ENDPOINT` | none: console only |
| `APIKey` | `VIEWER_API_KEY` | none: console only |
| `Service` | `VIEWER_SERVICE` | the executable's name |
| `Env` | `VIEWER_ENV` | `development` |
| `Level` | `VIEWER_LEVEL` | `info` |
| `Console` | – | text, or JSON lines when `Env` is `production`/`prod`; `ConsoleOff` to turn off |

Without an endpoint and an API key the logger writes to the console only, which suits
local development and tests. If only one of the two is set, it says so once on stderr.

## Behaviour to know

- **It never takes your program down.** A log call doesn't block or panic.
- **`Fatal` does not exit.** It records the log at the fatal level; your program decides
  what happens next. This differs from the standard library's `log.Fatal`.
- **No signal handling.** The SDK doesn't catch `SIGINT`/`SIGTERM`. Call `Close` before the
  program ends, for example with `signal.NotifyContext` and `defer log.Close(ctx)`.
- **No dependencies** outside the standard library.

Requires Go 1.23+. Licensed under MIT.
