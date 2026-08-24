package postgres

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// address is the triple a pool answers for: one database of one cluster. The
// cluster half is what keeps two clusters that both have an "orders" database
// from sharing a pool into whichever of them was reached first.
type address struct {
	cluster  mcpsrv.Address
	database string
}

func (a address) String() string { return a.cluster.String() + "/" + a.database }

// pools is the pool cache: one address is one pool, the least recently used of
// a cluster evicted past that cluster's pool.maxDatabases so that walking fifty
// databases cannot eat someone else's max_connections (ADR-0003). Closing is
// deferred by refcount — eviction never pulls a pool out from under a running
// call.
type pools struct {
	log *slog.Logger

	mu     sync.Mutex
	byAddr map[address]*entry
	lru    *list.List // *entry, most recently used at the front
	closed bool
}

type entry struct {
	addr address
	pool *pgxpool.Pool
	el   *list.Element
	refs int
	// dead means evicted or shut down: the last release closes the pool.
	dead bool
	// idleSince is when refs last reached zero; the sweep reads it.
	idleSince time.Time
	// idle is the idleTimeout of the cluster this pool belongs to: the sweep
	// runs over every cluster's pools at once, and they need not agree.
	idle time.Duration
}

func newPools(log *slog.Logger) *pools {
	return &pools{log: log, byAddr: make(map[address]*entry), lru: list.New()}
}

var errPoolsClosed = errors.New("the server is shutting down")

// acquire hands out the pool for addr and a release the caller must call. cfg
// is the config in effect at that address, and is read only when the pool has
// to be opened: the pool is created on first use and its failure to connect is
// not remembered — what is cached is the pool, and pgx dials again on the next
// call.
func (p *pools) acquire(ctx context.Context, cfg Config, addr address) (*pgxpool.Pool, func(), error) {
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

	e, ok := p.byAddr[addr]
	if ok {
		p.lru.MoveToFront(e.el)
	} else {
		pc, err := poolConfig(cfg, addr.database)
		if err != nil {
			return nil, nil, err
		}
		// Background, not ctx: the pool outlives the call that opened it, and
		// its health check goroutine takes this context.
		pool, err := pgxpool.NewWithConfig(context.Background(), pc) //nolint:contextcheck // the pool is not the call's to cancel
		if err != nil {
			return nil, nil, err
		}
		e = &entry{addr: addr, pool: pool, idleSince: time.Now(), idle: cfg.Pool.IdleTimeout.Duration()}
		e.el = p.lru.PushFront(e)
		p.byAddr[addr] = e
		p.log.Debug("pool opened", "address", addr, "open", len(p.byAddr))
	}

	// Before the overflow check: the pool this call came for is in use from
	// here, and an eviction that picks it is an eviction of nothing.
	e.refs++
	stale = append(stale, p.evictOverflow(addr.cluster, cfg.Pool.MaxDatabases)...)

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

// evictOverflow drops least-recently-used pools of one cluster until it is back
// within its pool.maxDatabases, returning the ones the caller must close. The
// limit is a cluster's, not the process's: a source has one shape of cluster
// config and no second, per-process one. Called with the lock held.
func (p *pools) evictOverflow(cluster mcpsrv.Address, limit int) []*pgxpool.Pool {
	var stale []*pgxpool.Pool
	for p.count(cluster) > max(limit, 1) {
		e := p.evictable(cluster)
		if e == nil {
			break
		}
		p.log.Info("pool evicted", "address", e.addr, "inUse", e.refs > 0, "limit", limit)
		if pool := p.drop(e); pool != nil {
			stale = append(stale, pool)
		}
	}
	return stale
}

// count is how many pools cluster holds. Called with the lock held.
func (p *pools) count(cluster mcpsrv.Address) int {
	n := 0
	for addr := range p.byAddr {
		if addr.cluster == cluster {
			n++
		}
	}
	return n
}

// evictable is the least recently used entry of cluster, preferring one nobody
// is using: evicting a busy pool is legal — the close waits — but it costs a
// reconnect the very next moment.
func (p *pools) evictable(cluster mcpsrv.Address) *entry {
	var busy *entry
	for el := p.lru.Back(); el != nil; el = el.Prev() {
		e, _ := el.Value.(*entry)
		if e.addr.cluster != cluster {
			continue
		}
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
	var stale []*pgxpool.Pool
	now := time.Now()
	for el := p.lru.Back(); el != nil; {
		e, _ := el.Value.(*entry)
		el = el.Prev()
		if e.idle > 0 && e.refs == 0 && now.Sub(e.idleSince) > e.idle {
			p.log.Debug("pool closed after idle", "address", e.addr, "idle", e.idle)
			if pool := p.drop(e); pool != nil {
				stale = append(stale, pool)
			}
		}
	}
	return stale
}

// forget drops the pool for addr when the call that opened it never got a
// connection. An entry that cannot connect is still an entry: three wrong
// database names would push the working pool out of a cache of four.
//
// pool is what says the entry is still the caller's own: an eviction can have
// replaced it since acquire — evicting a busy pool is legal — and the successor
// is healthy. Every entry gets its own pgxpool, so the pointer is its identity.
func (p *pools) forget(addr address, pool *pgxpool.Pool) {
	p.mu.Lock()
	var stale *pgxpool.Pool
	if e, ok := p.byAddr[addr]; ok {
		if e.pool == pool {
			stale = p.drop(e)
		} else {
			p.log.Debug("pool replaced before forget", "address", addr)
		}
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
	delete(p.byAddr, e.addr)
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
	return len(p.byAddr)
}
