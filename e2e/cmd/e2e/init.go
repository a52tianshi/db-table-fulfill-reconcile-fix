package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
)

func randOrder(rng *rand.Rand, id, user int64) store.Order {
	return store.Order{
		OrderID: id, UserID: user,
		Status:    int32(rng.Intn(5)),
		Amount:    rng.Int63n(1_000_000),
		Remark:    fmt.Sprintf("r%08x", rng.Uint32()),
		Version:   1,
		UpdatedAt: time.Now().UnixMicro(),
	}
}

func insertMany(ctx context.Context, db *sql.DB, table string, rows []store.Order) error {
	if len(rows) == 0 {
		return nil
	}
	vals := strings.TrimSuffix(strings.Repeat("("+store.Placeholders(7)+"),", len(rows)), ",")
	args := make([]any, 0, len(rows)*7)
	for _, o := range rows {
		args = append(args, o.Args()...)
	}
	_, err := db.ExecContext(ctx, "INSERT INTO "+table+" ("+store.Cols+") VALUES "+vals, args...)
	return err
}

func initCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	rows := fs.Int("rows", 200_000, "rows to insert into the old table")
	users := fs.Int64("users", 20_000, "distinct user ids")
	dist := fs.String("dist", "skew", "shard distribution: skew | uniform")
	copyRatio := fs.Float64("copy-ratio", 0, "optional: fraction of old rows pre-copied into new tables")
	dirtyRatio := fs.Float64("dirty-ratio", 0, "optional: fraction of pre-copied rows given a stale value")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed")
	return func(ctx context.Context, db *sql.DB) error {
		start := time.Now()
		if err := store.CreateSchema(ctx, db); err != nil {
			return err
		}
		plan, err := store.NewPlan(*dist, *rows, *users)
		if err != nil {
			return err
		}
		rng := rand.New(rand.NewSource(*seed))
		targets := plan.InitShards(rng)
		ids := &store.Snowflake{Worker: 0}
		const batch = 1000
		var copied, dirty int
		for done := 0; done < *rows; {
			n := min(batch, *rows-done)
			old := make([]store.Order, n)
			shards := map[string][]store.Order{}
			for i := range old {
				old[i] = randOrder(rng, ids.Next(), plan.User(rng, targets[done+i]))
				if rng.Float64() < *copyRatio {
					c := old[i]
					if rng.Float64() < *dirtyRatio {
						c.Remark, c.Version = "stale", 0
						dirty++
					}
					t := store.NewTable(c.UserID)
					shards[t] = append(shards[t], c)
					copied++
				}
			}
			if err := insertMany(ctx, db, store.OldTable, old); err != nil {
				return err
			}
			for t, rs := range shards {
				if err := insertMany(ctx, db, t, rs); err != nil {
					return err
				}
			}
			done += n
		}
		fmt.Printf("[init] seed=%d old=%d users=%d preCopied=%d preDirty=%d in %s\n[init] %s\n",
			*seed, *rows, *users, copied, dirty, time.Since(start).Round(time.Millisecond), plan.Describe())
		return nil
	}
}
