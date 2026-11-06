package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
)

func action(s string) (r.Action, error) {
	switch s {
	case "fix":
		return r.Fix, nil
	case "warn":
		return r.Warn, nil
	}
	return 0, fmt.Errorf("bad action %q (fix|warn)", s)
}

func reconcileCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	missing := fs.String("missing", "fix", "missing rows: fix | warn")
	different := fs.String("different", "fix", "different rows: fix | warn")
	insertMode := fs.String("insert", "batch-snapshot", "missing rows: per-row | batch-locked | batch-snapshot")
	batch := fs.Int("batch", 1000, "engine BatchSize (rows paired per lookup)")
	txBatch := fs.Int("tx-batch", 200, "engine TxBatchSize (keys per transaction)")
	page := fs.Int("page", 1000, "mysqltable PageSize (rows per scan query)")
	memLimit := fs.Int64("memory-limit", 0, "engine MemoryLimit in bytes (0 = BatchSize only)")
	ignore := fs.String("ignore", "", "comma separated Order fields allowed to differ, e.g. UpdatedAt")
	label := fs.String("label", "reconcile", "name printed in the report")
	expectClean := fs.Bool("expect-clean", false, "fail unless the run confirmed nothing and wrote nothing")
	expectNoMissing := fs.Bool("expect-no-missing", false, "fail on any confirmed missing/extra row (different allowed)")
	expectRepairs := fs.Bool("expect-repairs", false, "fail if nothing was written")
	return func(ctx context.Context, db *sql.DB) error {
		var policy r.Policy
		var err error
		if policy.Missing, err = action(*missing); err != nil {
			return err
		}
		if policy.Different, err = action(*different); err != nil {
			return err
		}
		modes := map[string]r.InsertMode{"per-row": r.InsertPerRow, "batch-locked": r.InsertBatchLocked, "batch-snapshot": r.InsertBatchSnapshot}
		mode, ok := modes[*insertMode]
		if !ok {
			return fmt.Errorf("bad -insert %q", *insertMode)
		}
		var ignored []string
		if *ignore != "" {
			ignored = strings.Split(*ignore, ",")
		}
		compare, err := r.Struct[store.Order](ignored)
		if err != nil {
			return err
		}
		pair, err := mysqltable.NewPair(db, store.Schema(), store.OldRouter(), store.NewRouter(), mysqltable.Options{
			PageSize:    *page,
			IsRetryable: store.Retryable,
			IsDuplicate: store.IsDuplicate,
		})
		if err != nil {
			return err
		}
		engine, err := r.New(r.Config[store.Order, store.Key]{
			Old:         pair.Old(),
			New:         pair.New(),
			Key:         store.KeyOf,
			Compare:     compare,
			Repairer:    pair.Repairer(),
			Notifier:    r.LogNotifier[store.Order, store.Key]{W: os.Stdout},
			Policy:      policy,
			Insert:      mode,
			BatchSize:   *batch,
			TxBatchSize: *txBatch,
			MemoryLimit: *memLimit,
			RowSize:     func(o store.Order) int64 { return int64(48 + len(o.Remark)) },
		})
		if err != nil {
			return err
		}
		rep, err := engine.Run(ctx)
		fmt.Printf("[%s] insert=%s %s\n", *label, mode, rep)
		if err != nil {
			return err
		}
		written := rep.Inserted + rep.Updated
		if *expectClean {
			if !rep.Consistent() || written > 0 {
				return fmt.Errorf("expected clean run")
			}
			fmt.Printf("[%s] PASS: nothing to repair (%d stale candidates resolved by reread)\n", *label, rep.Stale)
		}
		if *expectNoMissing {
			if rep.Missing+rep.Extra > 0 {
				return fmt.Errorf("expected no missing/extra rows")
			}
			fmt.Printf("[%s] PASS: no missing/extra; %d stale snapshot rows overwritten\n", *label, rep.Different)
		}
		if *expectRepairs && written == 0 {
			return fmt.Errorf("expected repairs, got none")
		}
		return nil
	}
}
