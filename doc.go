// Package viewer is the Go SDK for the Viewer logging platform.
//
// A Logger writes structured records to the console and sends them to Viewer over
// OTLP/HTTP. It never blocks or panics the program it runs in: records go into a bounded
// queue, and when Viewer can't be reached they are dropped and counted rather than held.
//
//	log := viewer.New(viewer.Config{Service: "billing", Env: "prod"})
//	defer log.Close(context.Background())
//
//	log.Info("invoice created", "invoice_id", id, "amount", 4200)
//	log.With("user_id", uid).Error("payment failed", "err", err)
//
// Settings not given in Config come from the environment: VIEWER_ENDPOINT,
// VIEWER_API_KEY, VIEWER_SERVICE, VIEWER_ENV and VIEWER_LEVEL. Without an endpoint and an
// API key the logger writes to the console only, which suits local development and tests.
//
// The SDK installs no signal handlers and never exits the process, not even from Fatal.
// Call Close before the program ends so queued records are sent.
package viewer
