package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
)

func reconcileCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	missing := fs.String("missing", "fix", "missing rows: fix | warn")
	different := fs.String("different", "fix", "different rows: fix | warn")
	insertMode := fs.String("insert", "batch-snapshot", "missing rows: per-row | batch-locked | batch-snapshot")
	page := fs.Int("page", 1000, "ScanPageSize (rows per scan query)")
	batch := fs.Int("batch", 1000, "BatchSize (rows paired per lookup)")
	txBatch := fs.Int("tx-batch", 200, "TxBatchSize (keys per transaction)")
	memLimit := fs.Int64("memory-limit", r.NoMemoryLimit, "MemoryLimit in bytes (-1 = none)")
	attempts := fs.Int("retry-attempts", 8, "Retry.MaxAttempts per step")
	backoff := fs.Duration("retry-backoff", 5*time.Millisecond, "Retry.Backoff")
	maxBackoff := fs.Duration("retry-max-backoff", 500*time.Millisecond, "Retry.MaxBackoff")
	progressEvery := fs.Duration("progress-every", 2*time.Second, "progress log interval (0 = no progress output)")
	failRate := fs.Float64("fail-rate", 0, "inject transient failures into this fraction of scan/lookup/tx calls")
	ignore := fs.String("ignore", "", "comma separated Order fields allowed to differ, e.g. UpdatedAt")
	label := fs.String("label", "reconcile", "name printed in the report")
	expectClean := fs.Bool("expect-clean", false, "fail unless the run confirmed nothing and wrote nothing")
	expectNoMissing := fs.Bool("expect-no-missing", false, "fail on any confirmed missing/extra row (different allowed)")
	expectRepairs := fs.Bool("expect-repairs", false, "fail if nothing was written")
	return func(ctx context.Context, db *sql.DB) error {
		actions := map[string]r.Action{"fix": r.Fix, "warn": r.Warn}
		modes := map[string]r.InsertMode{"per-row": r.InsertPerRow, "batch-locked": r.InsertBatchLocked, "batch-snapshot": r.InsertBatchSnapshot}
		var ignored []string
		if *ignore != "" {
			ignored = strings.Split(*ignore, ",")
		}
		compare, err := r.Struct[store.Order](ignored)
		if err != nil {
			return err
		}
		pair, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
			DB:          db,
			Schema:      store.Schema(),
			Old:         store.OldRouter(),
			New:         store.NewRouter(),
			TxOptions:   store.TxOpts(),
			InChunk:     1000,
			InsertChunk: 500,
			IsDuplicate: store.IsDuplicate,
		})
		if err != nil {
			return err
		}
		var rowSize func(store.Order) int64
		if *memLimit > 0 {
			rowSize = func(o store.Order) int64 { return int64(48 + len(o.Remark)) }
		}
		progress := r.NoProgress
		if *progressEvery > 0 {
			progress = r.LogProgress(os.Stdout, *progressEvery)
		}
		chaos := store.NewChaos(*failRate, time.Now().UnixNano())
		// Unknown flag values map to the zero value, which Run rejects.
		rep, err := r.Run(ctx, r.Config[store.Order, store.Key]{
			Old:        chaos.Source(pair.Old()),
			New:        chaos.Source(pair.New()),
			Key:        store.KeyOf,
			Compare:    compare,
			Policy:     r.Policy{Missing: actions[*missing], Different: actions[*different]},
			Insert:     modes[*insertMode],
			ExtraCheck: r.CheckExtra,
			Repairer:   chaos.Repairer(pair.Repairer()),
			Notifier:   r.LogNotifier[store.Order, store.Key]{W: os.Stdout},
			Progress:   progress,

			ScanPageSize: *page,
			BatchSize:    *batch,
			TxBatchSize:  *txBatch,
			MemoryLimit:  *memLimit,
			RowSize:      rowSize,
			Retry: r.Retry{
				MaxAttempts: *attempts,
				Backoff:     *backoff,
				MaxBackoff:  *maxBackoff,
				Retryable:   mysqltable.IsTransient,
			},
		})
		fmt.Printf("[%s] insert=%s injected=%d %s\n", *label, *insertMode, chaos.Injected(), rep)
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
