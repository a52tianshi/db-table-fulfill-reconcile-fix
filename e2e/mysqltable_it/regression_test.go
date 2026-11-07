package mysqltable_it

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
)

func regressionConfig(t *testing.T, p *mysqltable.Pair[store.Order, store.Key]) r.Config[store.Order, store.Key] {
	t.Helper()
	return r.Config[store.Order, store.Key]{
		Old: p.Old(), New: p.New(), Key: store.KeyOf,
		Compare:      r.Exact[store.Order](),
		Policy:       r.Policy{Missing: r.Fix, Different: r.Fix},
		Insert:       r.InsertBatchLocked,
		ExtraCheck:   r.CheckExtra,
		Repairer:     p.Repairer(),
		Notifier:     r.LogNotifier[store.Order, store.Key]{W: io.Discard},
		Progress:     r.NoProgress,
		ScanPageSize: 3, BatchSize: 5, TxBatchSize: 3,
		MemoryLimit: r.NoMemoryLimit,
		Retry: r.Retry{MaxAttempts: 3, Backoff: time.Millisecond, MaxBackoff: time.Millisecond,
			Retryable: mysqltable.IsTransient},
	}
}

func TestWrongShardStopsRunWithoutDeleting(t *testing.T) {
	for _, withOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "extra", true: "duplicate"}[withOld], func(t *testing.T) {
			db := openDB(t)
			o := order(1, 1)
			insert(t, db, newTable(0), o)
			if withOld {
				insert(t, db, "it_old", o)
				insert(t, db, newTable(1), o)
			}
			cfg := regressionConfig(t, newPair(t, db))
			cfg.ScanPageSize, cfg.BatchSize = 1, 1
			rep, err := r.Run(context.Background(), cfg)
			if !errors.Is(err, mysqltable.ErrMisrouted) {
				t.Fatalf("run must fail, not report successful consistency: %v %v", rep, err)
			}
			var detail *mysqltable.MisroutedError
			if !errors.As(err, &detail) || detail.Table != newTable(0) || detail.Expected != newTable(1) {
				t.Fatalf("details=%+v err=%v", detail, err)
			}
			var n int
			if err = db.QueryRow("SELECT COUNT(*) FROM " + newTable(0)).Scan(&n); err != nil || n != 1 {
				t.Fatalf("wrong shard row must remain, count=%d err=%v", n, err)
			}
			if rep.Retries != 0 {
				t.Fatalf("routing data error retried: %v", rep)
			}
		})
	}
}

func TestCorrectlyRoutedExtraWarnsAndRemains(t *testing.T) {
	db := openDB(t)
	o := order(1, 1)
	insert(t, db, newTable(1), o)
	p := newPair(t, db)
	rep, err := r.Run(context.Background(), regressionConfig(t, p))
	if err != nil || rep.Extra != 1 || rep.Warned != 1 || rep.Inserted+rep.Updated != 0 {
		t.Fatalf("%v %v", rep, err)
	}
	got, err := p.New().Lookup(context.Background(), []store.Key{store.KeyOf(o)})
	if err != nil || got[store.KeyOf(o)] != o {
		t.Fatalf("extra modified: %v %v", got, err)
	}
}

func TestLockSortsAcrossChunksAgainstOnlineWriter(t *testing.T) {
	db := openDB(t)
	a, b := order(1, 1), order(2, 1)
	insert(t, db, "it_old", a, b)
	p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
		DB:          db,
		Schema:      store.Schema(),
		Old:         mysqltable.Single[store.Key]("it_old"),
		New:         mysqltable.Mod("it_new_%d", 4, func(k store.Key) int64 { return k.UserID }),
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     1, // one key per IN query: lock order across chunks matters
		InsertChunk: 2,
		IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	online, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer online.Rollback()
	var id int64
	if err = online.QueryRowContext(ctx, "SELECT order_id FROM it_old WHERE order_id=? FOR UPDATE", a.OrderID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- p.Repairer().InTx(ctx, func(tx r.Tx[store.Order, store.Key]) error {
			_, _, err := tx.Lock(ctx, []store.Key{store.KeyOf(b), store.KeyOf(a)})
			return err
		})
	}()
	waiting := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
		select {
		case err := <-done:
			t.Fatalf("adapter unexpectedly finished before lock release: %v", err)
		default:
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM performance_schema.data_lock_waits").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("expected adapter to wait on first (ascending) key")
	}
	if err = online.QueryRowContext(ctx, "SELECT order_id FROM it_old WHERE order_id=? FOR UPDATE", b.OrderID).Scan(&id); err != nil {
		t.Fatalf("online writer must be able to lock key 2; adapter should wait on 1 first: %v", err)
	}
	if err = online.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("global ascending lock order must avoid the reproduced cycle: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestHighWaterIsKeyBoundNotTemporalSnapshot(t *testing.T) {
	db := openDB(t)
	insert(t, db, "it_old", order(1, 1), order(3, 1), order(5, 1))
	src := newPair(t, db).Old()
	rows, next, err := src.ScanPage(context.Background(), nil, 1)
	if err != nil || len(rows) != 1 || next == nil {
		t.Fatalf("%v %v", rows, err)
	}
	insert(t, db, "it_old", order(2, 1))
	rows, _, err = src.ScanPage(context.Background(), next, 1)
	if err != nil || len(rows) != 1 || rows[0].OrderID != base+2 {
		t.Fatalf("late insert below upper key bound may still appear: %v %v", rows, err)
	}
}

func TestWrongShardInLockRollsBackEarlierUpdate(t *testing.T) {
	db := openDB(t)
	oldBad := order(1, 1)
	good := order(2, 1)
	insert(t, db, "it_old", oldBad, good)
	// Physical table 1 contains a row that should route to table 2.
	insert(t, db, newTable(1), order(1, 2), good)
	p := newPair(t, db)
	changed := good
	changed.Remark = "must-roll-back"
	err := p.Repairer().InTx(context.Background(), func(tx r.Tx[store.Order, store.Key]) error {
		if err := tx.Update(context.Background(), changed); err != nil {
			return err
		}
		_, _, err := tx.Lock(context.Background(), []store.Key{store.KeyOf(oldBad)})
		return err
	})
	if !errors.Is(err, mysqltable.ErrMisrouted) {
		t.Fatalf("expected route failure from Lock: %v", err)
	}
	got, err := p.New().Lookup(context.Background(), []store.Key{store.KeyOf(good)})
	if err != nil || got[store.KeyOf(good)] != good {
		t.Fatalf("earlier update must roll back: %v %v", got, err)
	}
}
