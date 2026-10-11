package sharedhttp

import (
	"net/http"
	"time"
)

// Timeouts for the server and agent APIs. There is no WriteTimeout: a webhook
// waits up to the server's WebhookTimeout (100s) for the agents, and an agent
// task up to its TaskTimeout (60s); their own deadlines bound those handlers.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 2 * time.Minute
)

// NewServer returns an HTTP server for handler that drops clients which are
// slow to send a request or keep an idle connection open.
func NewServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}
}
