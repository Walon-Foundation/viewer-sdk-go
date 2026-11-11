# viewer-sdk-go

Go logger SDK for the Viewer logging platform: structured logging to the console and to
Viewer over OTLP/HTTP, a `slog.Handler`, and `net/http` middleware.

> Work in progress. The logger, its settings and the console output are in; sending to
> Viewer, the `slog` handler and the middleware follow.

```bash
go get github.com/Walon-Foundation/viewer-sdk-go@latest
```

```go
import viewer "github.com/Walon-Foundation/viewer-sdk-go"

log := viewer.New(viewer.Config{Service: "billing", Env: "prod"}) // the rest from env
defer log.Close(ctx)

log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200)
log.With("user_id", uid).Error("payment failed", "err", err)
```

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
