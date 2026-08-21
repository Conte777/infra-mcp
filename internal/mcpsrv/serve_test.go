package mcpsrv

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDrainWaitsForTheCallsInFlight(t *testing.T) {
	cancelled := false
	finished := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		finished <- nil
	}()

	err := drain(func() error { return <-finished }, func() { cancelled = true }, time.Second, discardLogger)
	if err != nil {
		t.Fatalf("drain() = %v", err)
	}
	if cancelled {
		t.Error("the calls were cancelled although they finished inside the grace period")
	}
}

func TestDrainCancelsWhenTheGraceExpires(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	stuck := make(chan error, 1)
	t.Cleanup(func() { stuck <- nil })

	err := drain(func() error { return <-stuck }, func() { cancelled <- struct{}{} }, time.Millisecond, discardLogger)
	if err != nil {
		t.Fatalf("drain() = %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Error("the grace period expired and the calls were left running")
	}
}

func TestServeHTTPStopsOnTheSignal(t *testing.T) {
	addr := freeAddr(t)
	spec := testSpec()
	server := Build(spec, NewRuntime(spec.Defaults, nil, testEnv(), nil))
	ctx, cancel := context.WithCancel(t.Context())

	ended := make(chan error, 1)
	go func() { ended <- serveHTTP(ctx, server, addr, discardLogger) }()

	waitForListener(t, addr)
	cancel()

	select {
	case err := <-ended:
		if err != nil && !isShutdown(err) {
			t.Fatalf("serveHTTP() = %v", err)
		}
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("serveHTTP did not return after the signal")
	}
}

func TestIsShutdownRejectsARealFailure(t *testing.T) {
	if isShutdown(errors.New("address already in use")) {
		t.Fatal("a failed listen would exit 0")
	}
}

// freeAddr picks a port by binding it and letting go: serveHTTP takes an
// address, not a listener, so port 0 would leave nothing to dial.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return addr
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	d := net.Dialer{Timeout: 100 * time.Millisecond}
	for range 100 {
		conn, err := d.DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}
