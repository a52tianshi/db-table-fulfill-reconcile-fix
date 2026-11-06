package store

import (
	"context"
	"errors"
	"math/rand"
	"sync"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

// ErrInjected looks like a MySQL deadlock so IsTransient retries it.
var ErrInjected = errors.New("Error 1213 (40001): injected deadlock")

// Chaos fails a fraction of calls BEFORE they reach MySQL, to exercise the
// engine's retry of every step (scan page, lookup, transaction).
type Chaos struct {
	Rate float64
	mu   sync.Mutex
	rng  *rand.Rand
	n    int64
}

func NewChaos(rate float64, seed int64) *Chaos {
	return &Chaos{Rate: rate, rng: rand.New(rand.NewSource(seed))}
}

func (c *Chaos) fail() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rng.Float64() < c.Rate {
		c.n++
		return true
	}
	return false
}

func (c *Chaos) Injected() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

type chaosSource struct {
	r.Source[Order, Key]
	c *Chaos
}

func (s chaosSource) ScanPage(ctx context.Context, cur r.Cursor, n int) ([]Order, r.Cursor, error) {
	if s.c.fail() {
		return nil, nil, ErrInjected
	}
	return s.Source.ScanPage(ctx, cur, n)
}

func (s chaosSource) Lookup(ctx context.Context, keys []Key) (map[Key]Order, error) {
	if s.c.fail() {
		return nil, ErrInjected
	}
	return s.Source.Lookup(ctx, keys)
}

type chaosRepairer struct {
	r.Repairer[Order, Key]
	g r.Grouper[Key]
	c *Chaos
}

func (p chaosRepairer) Group(k Key) string { return p.g.Group(k) }

// InTx fails either before the transaction or right before commit (after
// all its writes), so retried transactions really rerun their work.
func (p chaosRepairer) InTx(ctx context.Context, fn func(r.Tx[Order, Key]) error) error {
	if p.c.fail() {
		return ErrInjected
	}
	return p.Repairer.InTx(ctx, func(tx r.Tx[Order, Key]) error {
		if err := fn(tx); err != nil {
			return err
		}
		if p.c.fail() {
			return ErrInjected // rollback after writing
		}
		return nil
	})
}

func (c *Chaos) Source(s r.Source[Order, Key]) r.Source[Order, Key] {
	if c.Rate == 0 {
		return s
	}
	return chaosSource{s, c}
}

func (c *Chaos) Repairer(p r.Repairer[Order, Key]) r.Repairer[Order, Key] {
	if c.Rate == 0 {
		return p
	}
	return chaosRepairer{p, p.(r.Grouper[Key]), c}
}
