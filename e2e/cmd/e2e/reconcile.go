package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
)

func reconcileCmd(fs *flag.FlagSet) func(context.Context, *sql.DB) error {
	modeFlag := fs.String("mode", "repair", "repair | alert")
	batch := fs.Int("batch", 1000, "engine batch size")
	page := fs.Int("page", 1000, "keyset scan page size")
	insertMode := fs.String("insert", "batch-snapshot", "missing rows: row | batch-locked | batch-snapshot")
	expectNoMissing := fs.Bool("expect-no-missing", false, "fail if any confirmed missing/extra row (different allowed: stale snapshot inserts)")
	label := fs.String("label", "reconcile", "name printed in the report")
	expectClean := fs.Bool("expect-clean", false, "fail if any confirmed missing/different/extra row or repair (step 4)")
	expectRepairs := fs.Bool("expect-repairs", false, "fail if nothing was repaired (sanity check for step 3)")
	return func(ctx context.Context, db *sql.DB) error {
		mode := r.Repair
		switch *modeFlag {
		case "repair":
		case "alert":
			mode = r.AlertOnly
		default:
			return fmt.Errorf("bad -mode %q", *modeFlag)
		}
		rev := &store.Reviewer{DB: db, Log: os.Stdout}
		var reviewer r.Reviewer[store.Order, store.Key] = rev
		switch *insertMode {
		case "row":
		case "batch-locked":
			reviewer = store.BatchReviewer{Reviewer: rev, Locked: true}
		case "batch-snapshot":
			reviewer = store.BatchReviewer{Reviewer: rev}
		default:
			return fmt.Errorf("bad -insert %q", *insertMode)
		}
		e := r.Engine[store.Order, store.Key]{
			Old:       store.OldSource{DB: db, Page: *page},
			New:       store.NewSource{DB: db, Page: *page},
			Records:   store.Records{},
			Cache:     &r.MemoryCache[store.Order, store.Key]{Limit: *batch},
			Reviewer:  reviewer,
			BatchSize: *batch,
		}
		start := time.Now()
		st, err := e.Run(ctx, mode)
		fmt.Printf("[%s] mode=%s insert=%s oldScanned=%d newScanned=%d candidates=%d | confirmed: missing=%d different=%d extra=%d staleSkipped=%d | repaired=%d retries=%d | %s\n",
			*label, *modeFlag, *insertMode, st.OldScanned, st.NewScanned, st.Candidates,
			rev.Missing.Load(), rev.Different.Load(), rev.Extra.Load(), rev.Stale.Load(),
			rev.Repaired.Load(), rev.Retries.Load(), time.Since(start).Round(time.Millisecond))
		if err != nil {
			return err
		}
		if *expectClean {
			if n := rev.Missing.Load() + rev.Different.Load() + rev.Extra.Load() + rev.Repaired.Load(); n > 0 {
				return fmt.Errorf("expected clean run, found %d confirmed inconsistencies", n)
			}
			fmt.Printf("[%s] PASS: nothing to repair (%d stale candidates resolved by reread)\n", *label, rev.Stale.Load())
		}
		if *expectNoMissing {
			if n := rev.Missing.Load() + rev.Extra.Load(); n > 0 {
				return fmt.Errorf("expected no missing/extra rows, found %d", n)
			}
			fmt.Printf("[%s] PASS: no missing/extra; %d stale snapshot rows overwritten\n", *label, rev.Different.Load())
		}
		if *expectRepairs && rev.Repaired.Load() == 0 {
			return fmt.Errorf("expected repairs, got none")
		}
		return nil
	}
}
