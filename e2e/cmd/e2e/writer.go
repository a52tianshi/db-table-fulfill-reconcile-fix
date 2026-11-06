package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
)

// idPool holds known order ids so updates hit existing rows.
type idPool struct {
	mu  sync.RWMutex
	ids []store.Key
}

func (p *idPool) add(k store.Key) { p.mu.Lock(); p.ids = append(p.ids, k); p.mu.Unlock() }
func (p *idPool) pick(rng *rand.Rand) (store.Key, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.ids) == 0 {
		return store.Key{}, false
	}
	return p.ids[rng.Intn(len(p.ids))], true
}

// The simulated online code. Every write touches old and new in ONE
// transaction, old row first (same lock order as the Reviewer). An UPDATE on a
// not-yet-migrated row affects 0 rows in the new table; the framework fills it.
func dualInsert(ctx context.Context, db *sql.DB, o store.Order) error {
	tx, err := db.BeginTx(ctx, store.TxOpts())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := " (" + store.Cols + ") VALUES (" + store.Placeholders(7) + ")"
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+store.OldTable+q, o.Args()...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+store.NewTable(o.UserID)+q, o.Args()...); err != nil {
		return err
	}
	return tx.Commit()
}

func dualUpdate(ctx context.Context, db *sql.DB, rng *rand.Rand, k store.Key) error {
	tx, err := db.BeginTx(ctx, store.TxOpts())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := store.ScanOrder(tx.QueryRowContext(ctx,
		"SELECT "+store.Cols+" FROM "+store.OldTable+" WHERE order_id=? FOR UPDATE", k.OrderID))
	if err != nil {
		return err
	}
	o.Status = int32(rng.Intn(5))
	o.Amount += rng.Int63n(1000) - 500
	o.Remark = fmt.Sprintf("u%08x", rng.Uint32())
	o.Version++
	o.UpdatedAt = time.Now().UnixMicro()
	if _, err := tx.ExecContext(ctx, "UPDATE "+store.OldTable+" SET "+store.UpdateSet, o.MutableArgs()...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE "+store.NewTable(o.UserID)+" SET "+store.UpdateSet, o.MutableArgs()...); err != nil {
		return err
	}
	return tx.Commit()
}

func writerCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	workers := fs.Int("workers", 8, "concurrent writers")
	insertRatio := fs.Float64("insert-ratio", 0.3, "fraction of operations that insert a new order")
	users := fs.Int64("users", 20_000, "distinct user ids (should match init)")
	dist := fs.String("dist", "skew", "shard distribution for inserts (should match init)")
	pause := fs.Duration("pause", 0, "sleep between operations per worker (0 = full speed)")
	duration := fs.Duration("duration", 0, "stop after this long (0 = until SIGTERM/SIGINT)")
	return func(ctx context.Context, db *sql.DB) error {
		if *duration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *duration)
			defer cancel()
		}
		plan, err := store.NewPlan(*dist, 1<<30, *users)
		if err != nil {
			return err
		}
		pool := &idPool{}
		rows, err := db.QueryContext(ctx, "SELECT order_id,user_id FROM "+store.OldTable)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k store.Key
			if err := rows.Scan(&k.OrderID, &k.UserID); err != nil {
				rows.Close()
				return err
			}
			pool.ids = append(pool.ids, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		fmt.Printf("[writer] started workers=%d known=%d insertRatio=%.2f\n", *workers, len(pool.ids), *insertRatio)

		var inserts, updates, failed atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < *workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
				ids := &store.Snowflake{Worker: int64(w + 1)}
				for ctx.Err() == nil {
					var err error
					if k, ok := pool.pick(rng); ok && rng.Float64() >= *insertRatio {
						if err = dualUpdate(ctx, db, rng, k); err == nil {
							updates.Add(1)
						}
					} else {
						o := randOrder(rng, ids.Next(), plan.WriterUser(rng))
						if err = dualInsert(ctx, db, o); err == nil {
							inserts.Add(1)
							pool.add(store.KeyOf(o))
						}
					}
					// A failed request rolls back both sides, like a failed API call.
					if err != nil && ctx.Err() == nil {
						if failed.Add(1) <= 5 && !store.Retryable(err) {
							fmt.Printf("[writer] tx error: %v\n", err)
						}
					}
					if *pause > 0 {
						select {
						case <-ctx.Done():
						case <-time.After(*pause):
						}
					}
				}
			}(w)
		}
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		for {
			select {
			case <-tick.C:
				fmt.Printf("[writer] inserts=%d updates=%d failed=%d\n", inserts.Load(), updates.Load(), failed.Load())
			case <-done:
				fmt.Printf("[writer] stopped inserts=%d updates=%d failed=%d\n", inserts.Load(), updates.Load(), failed.Load())
				if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return nil
			}
		}
	}
}
