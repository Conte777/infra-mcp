package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// A pool opens no connection until something acquires one, so the cache can be
// exercised in full against an address nothing answers on.
func testPools(t *testing.T, maxDatabases int, idle time.Duration) *pools {
	t.Helper()

	cfg := Defaults()
	cfg.Connection = Connection{Host: "127.0.0.1", Port: 1, User: "app", SSLMode: SSLDisable}
	cfg.Databases.Default = "app_db"
	cfg.Pool.MaxDatabases = maxDatabases
	cfg.Pool.IdleTimeout = mcpsrv.Duration(idle)

	p := newPools(cfg, slog.New(slog.DiscardHandler))
	t.Cleanup(p.Close)
	return p
}

func mustAcquire(t *testing.T, p *pools, db string) (*pgxpool.Pool, func()) {
	t.Helper()

	pool, release, err := p.acquire(context.Background(), db)
	if err != nil {
		t.Fatalf("acquire %s: %v", db, err)
	}
	return pool, release
}

// closed reports whether the pool is gone: puddle answers a use of a closed
// pool with "closed pool", and a live pool here only ever fails to dial.
func closed(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pool.Ping(ctx)
	if err == nil {
		t.Fatal("nothing should answer on this address")
	}
	return strings.Contains(err.Error(), "closed pool")
}

func TestPoolsReuseOnePoolPerDatabase(t *testing.T) {
	p := testPools(t, 4, time.Minute)

	first, release1 := mustAcquire(t, p, "app_db")
	release1()
	second, release2 := mustAcquire(t, p, "app_db")
	defer release2()

	if first != second {
		t.Error("the same database got a second pool")
	}
	if p.open() != 1 {
		t.Errorf("open = %d, want 1", p.open())
	}
}

func TestPoolsEvictLeastRecentlyUsed(t *testing.T) {
	p := testPools(t, 2, time.Minute)

	a, releaseA := mustAcquire(t, p, "a")
	releaseA()
	_, releaseB := mustAcquire(t, p, "b")
	releaseB()
	// b is now the most recent; a is next in line.
	_, releaseC := mustAcquire(t, p, "c")
	defer releaseC()

	if p.open() != 2 {
		t.Fatalf("open = %d, want the cache back at pool.maxDatabases", p.open())
	}
	if _, ok := p.byDB["a"]; ok {
		t.Error("the least recently used database stayed")
	}
	if !closed(t, a) {
		t.Error("an evicted pool nobody holds must be closed at once")
	}
}

// Eviction while a call is running is the case the refcount exists for: the
// pool leaves the cache immediately and closes only once its user is done.
func TestPoolsDeferEvictionOfAPoolInUse(t *testing.T) {
	p := testPools(t, 1, time.Minute)

	a, releaseA := mustAcquire(t, p, "a")
	_, releaseB := mustAcquire(t, p, "b")
	defer releaseB()

	if _, ok := p.byDB["a"]; ok {
		t.Fatal("the evicted database stayed in the cache")
	}
	if closed(t, a) {
		t.Fatal("the pool was closed under a call that still holds it")
	}

	releaseA()
	if !closed(t, a) {
		t.Error("the last release must close an evicted pool")
	}
}

// With every pool busy there is nothing to evict without cost, so the least
// recently used one goes anyway rather than the acquire failing.
func TestPoolsEvictABusyPoolWhenNothingElseIsFree(t *testing.T) {
	p := testPools(t, 1, time.Minute)

	_, releaseA := mustAcquire(t, p, "a")
	defer releaseA()
	_, releaseB := mustAcquire(t, p, "b")
	defer releaseB()

	if p.open() != 1 {
		t.Errorf("open = %d, want 1", p.open())
	}
	if _, ok := p.byDB["b"]; !ok {
		t.Error("the database just acquired is the one that must stay")
	}
}

func TestPoolsCloseIdlePoolsOnTheNextAcquire(t *testing.T) {
	p := testPools(t, 4, time.Nanosecond)

	a, releaseA := mustAcquire(t, p, "a")
	releaseA()

	time.Sleep(time.Millisecond)
	_, releaseB := mustAcquire(t, p, "b")
	defer releaseB()

	if _, ok := p.byDB["a"]; ok {
		t.Error("an idle pool survived the sweep")
	}
	if !closed(t, a) {
		t.Error("a swept pool must be closed")
	}
}

func TestPoolsRefuseAcquireAfterClose(t *testing.T) {
	p := testPools(t, 4, time.Minute)
	p.Close()

	if _, _, err := p.acquire(context.Background(), "a"); !errors.Is(err, errPoolsClosed) {
		t.Errorf("acquire after Close = %v, want errPoolsClosed", err)
	}
}

// Shutdown must not block on a call that is still running: the core gives that
// call its grace period and the last release does the closing.
func TestPoolsCloseDefersToTheLastRelease(t *testing.T) {
	p := testPools(t, 4, time.Minute)

	a, releaseA := mustAcquire(t, p, "a")
	p.Close()

	if closed(t, a) {
		t.Fatal("Close pulled the pool out from under a running call")
	}
	releaseA()
	if !closed(t, a) {
		t.Error("the last release after Close must close the pool")
	}
}
