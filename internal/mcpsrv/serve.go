package mcpsrv

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shutdownGrace is how long a call already in flight may still finish after the
// signal.
const shutdownGrace = 5 * time.Second

// httpReadHeaderTimeout bounds a client that opens a connection and never
// finishes sending its request headers.
const httpReadHeaderTimeout = 10 * time.Second

// errDrainTimeout is a stop that outlived the grace period. The process leaves
// anyway; what it must not do afterwards is tear down what those calls still use.
var errDrainTimeout = errors.New("calls were still running when the grace period expired")

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
		return drain(ss.Close, shutdownGrace, log)
	}
}

// serveHTTP serves streamable HTTP. Stateless, because a session id would be
// state carried between requests and there is none to carry.
func serveHTTP(ctx context.Context, server *mcp.Server, addr string, log *slog.Logger) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return serveListener(ctx, server, ln, log)
}

// serveListener is serveHTTP once the address is bound, so that a test can hand
// over a listener instead of racing for a free port.
func serveListener(ctx context.Context, server *mcp.Server, ln net.Listener, log *slog.Logger) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: log},
	)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: httpReadHeaderTimeout}

	ended := make(chan error, 1)
	go func() { ended <- srv.Serve(ln) }()

	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
		return drain(func() error {
			drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
			defer cancel()
			return srv.Shutdown(drainCtx)
		}, shutdownGrace, log)
	}
}

// drain runs stop, which quits accepting requests and waits for the ones in
// flight, and gives up on it after grace. Giving up is all it can do: over
// stdio the SDK hands handlers a Done channel that never fires (jsonrpc2
// ConnectionConfig.PropagateCancellation), so no cancellation reaches them.
func drain(stop func() error, grace time.Duration, log *slog.Logger) error {
	log.Info("signal received, draining", "grace", grace)

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()

	select {
	case err := <-stopped:
		if errors.Is(err, context.DeadlineExceeded) {
			return errDrainTimeout
		}
		return err
	case <-time.After(grace):
		return errDrainTimeout
	}
}
