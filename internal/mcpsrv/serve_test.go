package mcpsrv

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDrainWaitsForTheCallsInFlight(t *testing.T) {
	finished := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		finished <- nil
	}()

	err := drain(func() error { return <-finished }, time.Second, discardLogger)
	if err != nil {
		t.Fatalf("drain() = %v, want the calls to have finished inside the grace period", err)
	}
}

func TestDrainGivesUpWhenTheGraceExpires(t *testing.T) {
	stuck := make(chan error, 1)
	t.Cleanup(func() { stuck <- nil })

	err := drain(func() error { return <-stuck }, time.Millisecond, discardLogger)
	if !errors.Is(err, errDrainTimeout) {
		t.Fatalf("drain() = %v, want errDrainTimeout", err)
	}
}

// http.Server.Shutdown reports an expired grace as its context's error, which
// has to reach serve as the same timeout stdio reports.
func TestDrainTreatsAShutdownDeadlineAsATimeout(t *testing.T) {
	err := drain(func() error { return context.DeadlineExceeded }, time.Second, discardLogger)

	if !errors.Is(err, errDrainTimeout) {
		t.Fatalf("drain() = %v, want errDrainTimeout", err)
	}
}

func TestServeListenerStopsOnTheSignal(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}

	spec := testSpec()
	server := Build(spec, NewRuntime(spec.Defaults, nil, testEnv(), nil))
	ctx, cancel := context.WithCancel(t.Context())

	ended := make(chan error, 1)
	go func() { ended <- serveListener(ctx, server, ln, discardLogger) }()

	dial(t, ln.Addr().String())
	cancel()

	select {
	case err := <-ended:
		if err != nil && !isShutdown(err) {
			t.Fatalf("serveListener() = %v", err)
		}
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("serveListener did not return after the signal")
	}
}

func TestIsShutdownRejectsARealFailure(t *testing.T) {
	if isShutdown(errors.New("address already in use")) {
		t.Fatal("a failed listen would exit 0")
	}
}

func dial(t *testing.T, addr string) {
	t.Helper()
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("nothing listening on %s: %v", addr, err)
	}
	_ = conn.Close()
}
