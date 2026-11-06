package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"time"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
)

// verify is deliberately independent of the framework. Inside ONE consistent
// snapshot (a single time slice) it checks:
//  1. every new-table row sits in shard user_id % 100,
//  2. every old row exists in its shard with identical columns,
//  3. count(old) == sum(count(new)).
//
// 1+2 give an injective old->new mapping; with 3 it is a bijection, i.e. no
// missing, different or extra rows anywhere.
func verifyCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	page := fs.Int("page", 2000, "page size")
	show := fs.Int("show", 20, "max mismatches to print")
	dist := fs.String("dist", "skew", "distribution used by init; skew also checks frozen shard sizes")
	return func(ctx context.Context, db *sql.DB) error {
		start := time.Now()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+store.OldTable+" LIMIT 1"); err != nil {
			return err // establishes the snapshot
		}
		count := func(q string) (n int64, err error) { err = tx.QueryRowContext(ctx, q).Scan(&n); return }

		oldCount, err := count("SELECT COUNT(*) FROM " + store.OldTable)
		if err != nil {
			return err
		}
		plan, err := store.NewPlan(*dist, 1<<30, 1)
		if err != nil {
			return err
		}
		var newCount, misrouted, empty, belowPage int64
		var sizes [store.Shards]int64
		for i := 0; i < store.Shards; i++ {
			t := store.ShardTable(i)
			n, err := count("SELECT COUNT(*) FROM " + t)
			if err != nil {
				return err
			}
			m, err := count(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE user_id %% %d <> %d", t, store.Shards, i))
			if err != nil {
				return err
			}
			newCount += n
			misrouted += m
			sizes[i] = n
			if n == 0 {
				empty++
			} else if n < 1000 {
				belowPage++
			}
		}
		minS, maxS := sizes[0], sizes[0]
		for _, n := range sizes {
			minS, maxS = min(minS, n), max(maxS, n)
		}
		fmt.Printf("[verify] new shard sizes: empty=%d below1000=%d min=%d max=%d\n", empty, belowPage, minS, maxS)
		badFrozen := 0
		for s, want := range plan.Frozen() {
			if sizes[s] != int64(want) {
				badFrozen++
				fmt.Printf("[verify] FROZEN %s has %d rows, want %d\n", store.ShardTable(s), sizes[s], want)
			}
		}

		var checked, missing, different int64
		report := func(format string, a ...any) {
			if missing+different <= int64(*show) {
				fmt.Printf("[verify] "+format+"\n", a...)
			}
		}
		last := int64(-1 << 63)
		for {
			olds, err := store.QueryOrders(ctx, tx, "SELECT "+store.Cols+" FROM "+store.OldTable+
				" WHERE order_id > ? ORDER BY order_id LIMIT ?", last, *page)
			if err != nil {
				return err
			}
			byShard := map[string][]any{}
			for _, o := range olds {
				t := store.NewTable(o.UserID)
				byShard[t] = append(byShard[t], o.OrderID)
			}
			got := map[int64]store.Order{}
			for t, ids := range byShard {
				rows, err := store.QueryOrders(ctx, tx, "SELECT "+store.Cols+" FROM "+t+
					" WHERE order_id IN ("+store.Placeholders(len(ids))+")", ids...)
				if err != nil {
					return err
				}
				for _, o := range rows {
					got[o.OrderID] = o
				}
			}
			for _, o := range olds {
				checked++
				n, ok := got[o.OrderID]
				switch {
				case !ok:
					missing++
					report("MISSING %d in %s", o.OrderID, store.NewTable(o.UserID))
				case n != o:
					different++
					report("DIFFERENT %d\n  old=%+v\n  new=%+v", o.OrderID, o, n)
				}
			}
			if len(olds) < *page {
				break
			}
			last = olds[len(olds)-1].OrderID
		}

		fmt.Printf("[verify] old=%d new=%d checked=%d missing=%d different=%d misrouted=%d extra=%d | %s\n",
			oldCount, newCount, checked, missing, different, misrouted, newCount-(checked-missing),
			time.Since(start).Round(time.Millisecond))
		if missing != 0 || different != 0 || misrouted != 0 || badFrozen != 0 || oldCount != newCount || checked != oldCount {
			return fmt.Errorf("old and new tables are NOT consistent")
		}
		fmt.Println("[verify] PASS: old and new tables are identical")
		return nil
	}
}
