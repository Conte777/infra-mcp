package postgres

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pools is the per-database pool cache: one database is one pool, the least
// recently used evicted past pool.maxDatabases so that walking fifty databases
// cannot eat someone else's max_connections (ADR-0003). Closing is deferred by
// refcount — eviction never pulls a pool out from under a running call.
type pools struct {
	cfg Config
	log *slog.Logger

	mu     sync.Mutex
	byDB   map[string]*entry
	lru    *list.List // *entry, most recently used at the front
	closed bool
}

type entry struct {
	db   string
	pool *pgxpool.Pool
	el   *list.Element
	refs int
	// dead means evicted or shut down: the last release closes the pool.
	dead bool
	// idleSince is when refs last reached zero; the sweep reads it.
	idleSince time.Time
}

func newPools(cfg Config, log *slog.Logger) *pools {
	return &pools{cfg: cfg, log: log, byDB: make(map[string]*entry), lru: list.New()}
}

var errPoolsClosed = errors.New("the server is shutting down")

// acquire hands out the pool for db and a release the caller must call. The
// pool is created on first use and its failure to connect is not remembered:
// what is cached is the pool, and pgx dials again on the next call.
func (p *pools) acquire(ctx context.Context, db string) (*pgxpool.Pool, func(), error) {
	// Registered before the unlock below, so it runs after it: closing a pool
	// waits for its connections to come back, and on a socket that stopped
	// answering the driver takes its cleanup timeout to give up. Under the lock
	// that wait would stop every other database too.
	var stale []*pgxpool.Pool
	defer func() {
		for _, pool := range stale {
			pool.Close()
		}
	}()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, nil, errPoolsClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	stale = append(stale, p.sweepIdle()...)

	e, ok := p.byDB[db]
	if ok {
		p.lru.MoveToFront(e.el)
	} else {
		pc, err := poolConfig(p.cfg, db)
		if err != nil {
			return nil, nil, err
		}
		// Background, not ctx: the pool outlives the call that opened it, and
		// its health check goroutine takes this context.
		pool, err := pgxpool.NewWithConfig(context.Background(), pc) //nolint:contextcheck // the pool is not the call's to cancel
		if err != nil {
			return nil, nil, err
		}
		e = &entry{db: db, pool: pool, idleSince: time.Now()}
		e.el = p.lru.PushFront(e)
		p.byDB[db] = e
		p.log.Debug("pool opened", "database", db, "open", len(p.byDB))
	}

	// Before the overflow check: the pool this call came for is in use from
	// here, and an eviction that picks it is an eviction of nothing.
	e.refs++
	stale = append(stale, p.evictOverflow()...)

	return e.pool, func() { p.release(e) }, nil
}

func (p *pools) release(e *entry) {
	p.mu.Lock()
	e.refs--
	if e.refs == 0 {
		e.idleSince = time.Now()
	}
	closeNow := e.dead && e.refs == 0
	p.mu.Unlock()

	// Outside the lock: Close waits for connections to come back, and with no
	// references left there is nothing to wait for — but nothing here needs the
	// lock held to find that out.
	if closeNow {
		e.pool.Close()
	}
}

// evictOverflow drops least-recently-used pools until the cache is back within
// pool.maxDatabases, returning the ones the caller must close. Called with the
// lock held.
func (p *pools) evictOverflow() []*pgxpool.Pool {
	var stale []*pgxpool.Pool
	for len(p.byDB) > max(p.cfg.Pool.MaxDatabases, 1) {
		e := p.evictable()
		if e == nil {
			break
		}
		p.log.Info("pool evicted", "database", e.db, "inUse", e.refs > 0,
			"limit", p.cfg.Pool.MaxDatabases)
		if pool := p.drop(e); pool != nil {
			stale = append(stale, pool)
		}
	}
	return stale
}

// evictable is the least recently used entry, preferring one nobody is using:
// evicting a busy pool is legal — the close waits — but it costs a reconnect
// the very next moment.
func (p *pools) evictable() *entry {
	var busy *entry
	for el := p.lru.Back(); el != nil; el = el.Prev() {
		e, _ := el.Value.(*entry)
		if e.refs == 0 {
			return e
		}
		if busy == nil {
			busy = e
		}
	}
	return busy
}

// sweepIdle closes pools nobody has used for pool.idleTimeout. No janitor
// goroutine: the driver closes the idle connections themselves, and what is
// left here is bookkeeping the next call can just as well reclaim.
// Called with the lock held.
func (p *pools) sweepIdle() []*pgxpool.Pool {
	idle := p.cfg.Pool.IdleTimeout.Duration()
	if idle <= 0 {
		return nil
	}
	var stale []*pgxpool.Pool
	now := time.Now()
	for el := p.lru.Back(); el != nil; {
		e, _ := el.Value.(*entry)
		el = el.Prev()
		if e.refs == 0 && now.Sub(e.idleSince) > idle {
			p.log.Debug("pool closed after idle", "database", e.db, "idle", idle)
			if pool := p.drop(e); pool != nil {
				stale = append(stale, pool)
			}
		}
	}
	return stale
}

// forget drops the pool for db when the call that opened it never got a
// connection. An entry that cannot connect is still an entry: three wrong
// database names would push the working pool out of a cache of four.
func (p *pools) forget(db string) {
	p.mu.Lock()
	var stale *pgxpool.Pool
	if e, ok := p.byDB[db]; ok {
		stale = p.drop(e)
	}
	p.mu.Unlock()

	if stale != nil {
		stale.Close()
	}
}

// drop takes an entry out of the cache and hands back the pool to close, or nil
// when someone still holds it — then the last release closes it. Closing is the
// caller's job because it happens outside the lock. Called with the lock held.
func (p *pools) drop(e *entry) *pgxpool.Pool {
	delete(p.byDB, e.db)
	p.lru.Remove(e.el)
	e.dead = true
	if e.refs > 0 {
		return nil
	}
	return e.pool
}

// Close drops every pool. A pool still in use is closed by whoever releases it
// last, and the rest are closed without being waited for: the process is
// leaving, and a pool whose server stopped answering takes the driver's cleanup
// timeout to give up.
func (p *pools) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	for el := p.lru.Back(); el != nil; {
		e, _ := el.Value.(*entry)
		el = el.Prev()
		if pool := p.drop(e); pool != nil {
			go pool.Close()
		}
	}
}

// open is how many pools the cache holds; tests read it.
func (p *pools) open() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byDB)
}
