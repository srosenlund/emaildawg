package connector

import (
	"context"
	"net"
	"net/http"
	"time"
)

const graphWebhookPort = "29319"

// startGraphWebhookServer starts a dedicated HTTP server on :29319 for
// Microsoft Graph change notifications. It is independent of the appservice
// mux so it works in Beeper bbctl websocket-mode where the appservice never
// starts an inbound HTTP listener.
//
// Routes:
//
//	POST /_email/graph/webhook  — Graph change notifications + validation (POST handshake)
//	GET  /_email/graph/webhook  — Graph subscription-validation handshake (GET form)
//	GET  /_matrix/mau/live      — Sliplane / Beeper healthcheck → 200 OK
//	GET  /health                — belt-and-suspenders healthcheck → 200 OK
//
// The server shuts down cleanly when ctx is cancelled.
// Returns an error if the port cannot be bound (caller should log and continue).
func (ec *EmailConnector) startGraphWebhookServer(ctx context.Context) error {
	log := ec.Bridge.Log.With().Str("component", "graph_httpserver").Logger()

	mux := http.NewServeMux()

	// Graph webhook — both POST (notifications) and GET (validation handshake).
	mux.HandleFunc("POST /_email/graph/webhook", ec.handleGraphWebhookFull)
	mux.HandleFunc("GET /_email/graph/webhook", ec.handleGraphWebhookFull)

	// Healthcheck endpoints.
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /_matrix/mau/live", ok)
	mux.HandleFunc("GET /health", ok)

	srv := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", graphWebhookPort),
		Handler: mux,
	}

	// Bind the port synchronously so the caller can rely on :29319 being
	// ready before ensureGraphSubscription fires the validation POST.
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", graphWebhookPort))
	if err != nil {
		log.Error().Err(err).Msg("Graph webhook server: failed to bind port")
		return err
	}
	log.Info().Str("addr", listener.Addr().String()).Msg("Graph webhook server listening on :29319")

	// Shutdown the server when the context is cancelled.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Warn().Err(err).Msg("Graph webhook server: shutdown error")
		}
	}()

	// Serve on the already-bound listener in a goroutine so Start() is non-blocking.
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Graph webhook server exited with error")
		}
	}()

	return nil
}

