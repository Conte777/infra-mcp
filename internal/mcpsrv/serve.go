package mcpsrv

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shutdownGrace is how long a call already in flight may still finish after the
// signal. Past it the process stops waiting: a handler deaf to cancellation is
// not something the core can fix.
const shutdownGrace = 5 * time.Second

// httpReadHeaderTimeout bounds a client that opens a connection and never
// finishes sending its request headers.
const httpReadHeaderTimeout = 10 * time.Second

// serveStdio runs the server over stdin/stdout. The session context is
// deliberately not ctx: cancelling it would kill the calls in flight at the
// instant of the signal, which is the opposite of a graceful stop.
func serveStdio(ctx context.Context, server *mcp.Server, log *slog.Logger) error {
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	ss, err := server.Connect(sessionCtx, &mcp.StdioTransport{}, nil)
	if err != nil {
		return err
	}

	ended := make(chan error, 1)
	go func() { ended <- ss.Wait() }()

	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
		return drain(ss.Close, cancel, shutdownGrace, log)
	}
}

// drain runs stop, which quits accepting requests and waits for the ones in
// flight, and gives up on it after grace by cancelling them.
func drain(stop func() error, cancel context.CancelFunc, grace time.Duration, log *slog.Logger) error {
	log.Info("signal received, draining", "grace", grace)

	closed := make(chan error, 1)
	go func() { closed <- stop() }()

	select {
	case err := <-closed:
		return err
	case <-time.After(grace):
		log.Warn("calls still running after the grace period; cancelling them")
		cancel()
		return nil
	}
}

// serveHTTP serves streamable HTTP. Stateless, because a session id would be
// state carried between requests and there is none to carry.
func serveHTTP(ctx context.Context, server *mcp.Server, addr string, log *slog.Logger) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: log},
	)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: httpReadHeaderTimeout}

	ended := make(chan error, 1)
	go func() { ended <- srv.ListenAndServe() }()

	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
		log.Info("signal received, draining", "grace", shutdownGrace)
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		return srv.Shutdown(drainCtx)
	}
}
