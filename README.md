# viewer-sdk-go

Go logger SDK for the Viewer logging platform: structured logging over OTLP/HTTP, a `slog.Handler`, and `net/http` middleware.

> Work in progress: the API below is the planned shape.

```bash
go get github.com/Walon-Foundation/viewer-sdk-go@latest
```

```go
import viewer "github.com/Walon-Foundation/viewer-sdk-go"

log := viewer.New(viewer.Config{Service: "billing", Env: "prod"}) // VIEWER_ENDPOINT / VIEWER_API_KEY from env
defer log.Close(ctx)

log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200)
slog.SetDefault(slog.New(log.Handler()))           // existing slog code just works
http.ListenAndServe(":8080", log.Middleware(mux))  // any net/http-compatible router
```

Requires Go 1.22+. Licensed under MIT.
