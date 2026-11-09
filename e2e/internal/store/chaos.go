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

type injectedError struct {
	msg string
	err error
}

func (e injectedError) Error() string { return e.msg }
func (e injectedError) Unwrap() error { return e.err }

var transientPool = []error{
	ErrInjected,
	injectedError{"Error 1205 (HY000): injected lock wait timeout", ErrInjected},
	injectedError{"Error 2006 (HY000): injected server gone away", ErrInjected},
	injectedError{"Error 2013 (HY000): injected lost connection", ErrInjected},
}

// ChaosStats breaks down injected failures by execution stage.
type ChaosStats struct {
	Scan        int64
	Lookup      int64
	TxBegin     int64
	TxPreCommit int64
	Notify      int64
}

// Total returns the total number of injected failures across all stages.
func (s ChaosStats) Total() int64 {
	return s.Scan + s.Lookup + s.TxBegin + s.TxPreCommit + s.Notify
}

// Chaos fails a fraction of calls BEFORE they reach MySQL (or right before
// transaction commit), to exercise the engine's retry of every step
// (scan page, lookup, transaction begin/commit, notifier).
type Chaos struct {
	Rate  float64
	mu    sync.Mutex
	rng   *rand.Rand
	n     int64
	stats ChaosStats
}

func NewChaos(rate float64, seed int64) *Chaos {
	return &Chaos{Rate: rate, rng: rand.New(rand.NewSource(seed))}
}

func (c *Chaos) failStage(counter *int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rng.Float64() < c.Rate {
		c.n++
		*counter++
		return transientPool[c.rng.Intn(len(transientPool))]
	}
	return nil
}

func (c *Chaos) Injected() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func (c *Chaos) Stats() ChaosStats { c.mu.Lock(); defer c.mu.Unlock(); return c.stats }

type chaosSource struct {
	r.Source[Order, Key]
	c *Chaos
}

func (s chaosSource) ScanPage(ctx context.Context, cur r.Cursor, n int) ([]Order, r.Cursor, error) {
	if err := s.c.failStage(&s.c.stats.Scan); err != nil {
		return nil, nil, err
	}
	return s.Source.ScanPage(ctx, cur, n)
}

func (s chaosSource) Lookup(ctx context.Context, keys []Key) (map[Key]Order, error) {
	if err := s.c.failStage(&s.c.stats.Lookup); err != nil {
		return nil, err
	}
	return s.Source.Lookup(ctx, keys)
}

type chaosRepairer struct {
	r.Repairer[Order, Key]
	g r.Grouper[Key]
	c *Chaos
}

func (p chaosRepairer) Group(k Key) string {
	if p.g == nil {
		return ""
	}
	return p.g.Group(k)
}

// InTx fails either before the transaction or right before commit (after
// all its writes), so retried transactions really rerun their work.
func (p chaosRepairer) InTx(ctx context.Context, fn func(r.Tx[Order, Key]) error) error {
	if err := p.c.failStage(&p.c.stats.TxBegin); err != nil {
		return err
	}
	return p.Repairer.InTx(ctx, func(tx r.Tx[Order, Key]) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := p.c.failStage(&p.c.stats.TxPreCommit); err != nil {
			return err // rollback after writing
		}
		return nil
	})
}

type chaosNotifier struct {
	r.Notifier[Order, Key]
	c *Chaos
}

func (n chaosNotifier) Notify(ctx context.Context, f r.Finding[Order, Key]) error {
	if err := n.c.failStage(&n.c.stats.Notify); err != nil {
		return err
	}
	return n.Notifier.Notify(ctx, f)
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
	g, _ := p.(r.Grouper[Key])
	return chaosRepairer{p, g, c}
}

func (c *Chaos) Notifier(n r.Notifier[Order, Key]) r.Notifier[Order, Key] {
	if c.Rate == 0 {
		return n
	}
	return chaosNotifier{n, c}
}
