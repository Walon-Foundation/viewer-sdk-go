package viewer_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	viewer "github.com/Walon-Foundation/viewer-sdk-go"
)

func Example() {
	// VIEWER_ENDPOINT and VIEWER_API_KEY come from the environment.
	log := viewer.New(viewer.Config{Service: "billing", Env: "prod"})
	defer log.Close(context.Background())

	log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200)
	log.With("user_id", "u_42").Error("payment failed", "err", errors.New("card declined"))
}

func ExampleLogger_Handler() {
	log := viewer.New(viewer.Config{Service: "billing"})
	defer log.Close(context.Background())

	// Existing slog code now sends to Viewer too.
	slog.SetDefault(slog.New(log.Handler()))
	slog.Info("cache warmed", "entries", 1200)
}

func ExampleLogger_Middleware() {
	log := viewer.New(viewer.Config{Service: "billing"})
	defer log.Close(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /invoices", func(w http.ResponseWriter, r *http.Request) {
		// Carries this request's request_id and trace ids.
		viewer.FromContext(r.Context()).Info("creating invoice")
		w.WriteHeader(http.StatusCreated)
	})
	handler := log.MiddlewareWith(viewer.SkipPaths("/healthz"))(mux)
	_ = http.ListenAndServe(":8080", handler)
}

// Flush on shutdown: the SDK installs no signal handlers of its own.
func ExampleLogger_Close() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := viewer.New(viewer.Config{Service: "worker"})
	defer log.Close(context.Background()) // sends what is queued, waiting at most 5 s

	<-ctx.Done()
	log.Info("shutting down")
}
