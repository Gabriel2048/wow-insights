package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// The server's timeouts. Each one closes a way for a slow or hostile client
// to hold a goroutine open for free.
const (
	// readHeaderTimeout is how long a client has to finish sending headers.
	// This is the Slowloris bound: without it a handful of connections
	// dribbling one header byte at a time occupy the server indefinitely.
	readHeaderTimeout = 5 * time.Second
	// readTimeout bounds the whole request read, body included. Nothing here
	// takes a body, so this is generous.
	readTimeout = 15 * time.Second
	// idleTimeout is how long a keep-alive connection may sit between
	// requests before it is closed.
	idleTimeout = time.Minute
	// maxHeaderBytes is well above any legitimate header set for this app
	// and well below net/http's 1 MB default.
	maxHeaderBytes = 16 << 10

	// shutdownBudget is how long in-flight requests get to finish after the
	// process is told to stop. Cloud Run gives a revision ten seconds between
	// SIGTERM and SIGKILL; this stays strictly inside that so the process is
	// gone on its own terms rather than killed.
	shutdownBudget = 8 * time.Second
)

// Run serves the routes on addr until ctx is cancelled, then drains: new
// connections are refused, in-flight requests get shutdownBudget to finish,
// and Run returns nil. This is the one place an http.Server is built, so the
// shipped binary and the recorded-server both get the same timeouts and the
// same shutdown.
//
// WriteTimeout is deliberately left at zero. It is an absolute deadline
// measured from the end of the header read, so any value would hard-cap the
// long renders #2 plans; the work is bounded per handler instead.
func (s *Server) Run(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	return s.serve(ctx, ln)
}

// serve is Run past the listen, so a test can hand in a listener on port 0.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelError),
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		// Serve only returns on a listener failure; ErrServerClosed cannot
		// happen here because Shutdown has not been called.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "drain_budget", shutdownBudget.String())
	drain, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		// The budget ran out with requests still in flight; they are cut off
		// now, which is what would have happened at t=0 without Shutdown.
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
