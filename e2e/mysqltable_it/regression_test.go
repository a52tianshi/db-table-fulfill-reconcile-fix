package mysqltable_it

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
	"github.com/go-sql-driver/mysql"
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

func TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks(t *testing.T) {
	db := openDB(t)
	orig := order(1, 1)
	stale := order(1, 1)
	stale.Remark = "stale"
	insert(t, db, "it_old", orig)
	insert(t, db, newTable(1), stale)

	t.Run("short Values does not panic under lock and rolls back", func(t *testing.T) {
		schema := store.Schema()
		schema.Values = func(o store.Order) []any { return o.Args()[:3] } // 3 < 7 columns
		p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
			DB: db, Schema: schema,
			Old:       mysqltable.Single[store.Key]("it_old"),
			New:       mysqltable.Mod("it_new_%d", shards, func(k store.Key) int64 { return k.UserID }),
			TxOptions: &sql.TxOptions{Isolation: sql.LevelReadCommitted},
			InChunk:   3, InsertChunk: 2, IsDuplicate: mysqltable.IsDuplicateKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Run panicked inside locked transaction: %v", rec)
			}
		}()
		cfg := regressionConfig(t, p)
		rep, err := r.Run(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected error from short Values, got nil (report=%v)", rep)
		}
		if !errors.Is(err, mysqltable.ErrInvalidValues) || mysqltable.IsTransient(err) {
			t.Fatalf("want permanent ErrInvalidValues, got %v", err)
		}
		if rep.Retries != 0 {
			t.Fatalf("schema/values mismatch must not be retried, retries=%d", rep.Retries)
		}
		// Verify row lock on it_old was released by rollback.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 1"); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE it_old SET remark='r' WHERE order_id=?", orig.OrderID); err != nil {
			t.Fatalf("row lock on it_old was not released: %v", err)
		}
	})

	t.Run("long Values rejected instead of silent truncation", func(t *testing.T) {
		schema := store.Schema()
		schema.Values = func(o store.Order) []any { return append(o.Args(), "extra-col") } // 8 > 7 columns
		p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
			DB: db, Schema: schema,
			Old:       mysqltable.Single[store.Key]("it_old"),
			New:       mysqltable.Mod("it_new_%d", shards, func(k store.Key) int64 { return k.UserID }),
			TxOptions: &sql.TxOptions{Isolation: sql.LevelReadCommitted},
			InChunk:   3, InsertChunk: 2, IsDuplicate: mysqltable.IsDuplicateKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg := regressionConfig(t, p)
		rep, err := r.Run(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected error from long Values, got nil (report=%v)", rep)
		}
		if !errors.Is(err, mysqltable.ErrInvalidValues) || mysqltable.IsTransient(err) {
			t.Fatalf("want permanent ErrInvalidValues, got %v", err)
		}
		got, err := newPair(t, db).New().Lookup(context.Background(), []store.Key{store.KeyOf(orig)})
		if err != nil || got[store.KeyOf(orig)].Remark != "stale" {
			t.Fatalf("new row was modified despite invalid Values: %+v err=%v", got, err)
		}
	})
}

// Ported from the codex self-test branch. One row is short (6 Values) and the
// next is long (8 Values): the total placeholder count of the 2-row INSERT
// matches, so database/sql and MySQL accept it and, without per-row
// validation, commit shifted columns (b.Remark "123" lands in amount, etc.).
func TestInsertRejectsCompensatingValuesLengths(t *testing.T) {
	db := openDB(t)
	schema := store.Schema()
	schema.Values = func(o store.Order) []any {
		if o.OrderID == base+1 {
			return o.Args()[:6]
		}
		return append(o.Args(), int64(42))
	}
	p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
		DB: db, Schema: schema,
		Old:       mysqltable.Single[store.Key]("it_old"),
		New:       mysqltable.Mod("it_new_%d", shards, func(k store.Key) int64 { return k.UserID }),
		TxOptions: &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:   3, InsertChunk: 2, IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, b := order(1, 1), order(2, 1)
	b.Remark = "123" // shifted into a BIGINT column it is still valid SQL
	err = p.Repairer().InTx(context.Background(), func(tx r.Tx[store.Order, store.Key]) error {
		return tx.Insert(context.Background(), []store.Order{a, b})
	})
	if !errors.Is(err, mysqltable.ErrInvalidValues) || mysqltable.IsTransient(err) {
		t.Errorf("each row must have seven Values even when total args match: %v", err)
	}
	rows, _, scanErr := newPair(t, db).New().ScanPage(context.Background(), nil, 10)
	if scanErr != nil || len(rows) != 0 {
		t.Fatalf("malformed insert must leave no rows: rows=%+v err=%v", rows, scanErr)
	}
}

func TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter(t *testing.T) {
	db := openDB(t)
	for _, tb := range []string{"it_old_0", "it_old_1"} {
		for _, q := range []string{"DROP TABLE IF EXISTS " + tb, "CREATE TABLE " + tb + ` (
  order_id BIGINT NOT NULL PRIMARY KEY, user_id BIGINT NOT NULL, status INT NOT NULL,
  amount BIGINT NOT NULL, remark VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
  updated_at BIGINT NOT NULL) ENGINE=InnoDB`} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + tb) })
	}
	// Shard 0 has key 2 (higher order_id); shard 1 has key 1 (lower order_id).
	// Scanning Old reads shard 0 then shard 1 -> batch has [order(2,1), order(1,1)].
	a, b := order(1, 1), order(2, 1)
	insert(t, db, "it_old_0", b)
	insert(t, db, "it_old_1", a)

	p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
		DB:          db,
		Schema:      store.Schema(),
		Old:         mysqltable.Mod("it_old_%d", 2, func(k store.Key) int64 { return (k.OrderID - base) % 2 }),
		New:         mysqltable.Mod("it_new_%d", shards, func(k store.Key) int64 { return k.UserID }),
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     1,
		InsertChunk: 1, // one row per INSERT: order of INSERT statements determines lock acquisition order
		IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Online writer locks key 1 first in it_new_1 (by inserting it in an uncommitted tx),
	// then will attempt to lock/insert key 2 in ascending primary-key order.
	online, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer online.Rollback()
	if _, err := online.ExecContext(ctx, "INSERT INTO "+newTable(1)+" ("+store.Cols+") VALUES (?,?,?,?,?,?,?)", a.Args()...); err != nil {
		t.Fatal(err)
	}

	cfg := regressionConfig(t, p)
	cfg.Insert = r.InsertBatchSnapshot
	cfg.ExtraCheck = r.SkipExtra
	cfg.ScanPageSize, cfg.BatchSize, cfg.TxBatchSize = 10, 10, 10
	cfg.Retry.MaxAttempts = 1 // surface any deadlock directly without masking via retry

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, cfg)
		done <- err
	}()
	defer func() {
		_ = online.Rollback()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	waiting := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		select {
		case err := <-done:
			t.Fatalf("Run finished before online writer released lock: %v", err)
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
		time.Sleep(15 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("expected snapshot insert to wait on locked first (ascending) key")
	}
	// Online writer now inserts key 2 in ascending order. If Tx.Insert did not sort by KeyLess,
	// Tx.Insert already locked key 2 first and is waiting on key 1 -> DEADLOCK (Error 1213)!
	if _, err := online.ExecContext(ctx, "INSERT INTO "+newTable(1)+" ("+store.Cols+") VALUES (?,?,?,?,?,?,?)", b.Args()...); err != nil {
		t.Fatalf("online writer deadlocked or failed on key 2 because Tx.Insert locked key 2 before key 1: %v", err)
	}
	if err := online.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run failed after online rollback: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestRouteReturningUnlistedTableStopsWithoutWriting(t *testing.T) {
	db := openDB(t)
	o := order(1, 3) // user_id % 4 == 3 -> routes to it_new_3
	insert(t, db, "it_old", o)

	// New.Tables() only declares shards 0..2, omitting it_new_3, while Route can return it_new_3.
	badNew := mysqltable.RouterFunc([]string{newTable(0), newTable(1), newTable(2)}, func(k store.Key) string {
		return newTable(int(k.UserID % shards))
	})
	p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
		DB:          db,
		Schema:      store.Schema(),
		Old:         mysqltable.Single[store.Key]("it_old"),
		New:         badNew,
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     3,
		InsertChunk: 2,
		IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(context.Background(), regressionConfig(t, p))
	if !errors.Is(err, mysqltable.ErrMisrouted) {
		t.Fatalf("expected ErrMisrouted when Route(k) returns table outside Tables(), got rep=%v err=%v", rep, err)
	}
	if rep.Retries != 0 {
		t.Fatalf("out-of-bounds route must not be retried, retries=%d", rep.Retries)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + newTable(3)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unlisted table %s was written to: count=%d err=%v", newTable(3), count, err)
	}
}

type postCommitFaultRepairer struct {
	r.Repairer[store.Order, store.Key]
	g       r.Grouper[store.Key]
	onFirst func() error
	called  bool
}

func (p *postCommitFaultRepairer) Group(k store.Key) string { return p.g.Group(k) }

func (p *postCommitFaultRepairer) InTx(ctx context.Context, fn func(r.Tx[store.Order, store.Key]) error) error {
	if err := p.Repairer.InTx(ctx, fn); err != nil {
		return err
	}
	if !p.called {
		p.called = true
		return p.onFirst()
	}
	return nil
}

func TestRealMySQLLostCommitAckOnSnapshotInsertRecoversWithConcurrentUpdate(t *testing.T) {
	db := openDB(t)
	a, b := order(1, 1), order(2, 1)
	insert(t, db, "it_old", a, b)
	p := newPair(t, db)
	baseRep := p.Repairer()

	// After the first snapshot-insert transaction commits in MySQL for real,
	// an online writer updates row `a` in it_old and the caller receives a
	// lost-COMMIT-ack network error (2013).
	faultRep := &postCommitFaultRepairer{
		Repairer: baseRep,
		g:        baseRep.(r.Grouper[store.Key]),
		onFirst: func() error {
			if _, err := db.Exec("UPDATE it_old SET remark='fresh-after-ack-loss', version=2 WHERE order_id=?", a.OrderID); err != nil {
				t.Fatalf("concurrent old update: %v", err)
			}
			return errors.New("Error 2013 (HY000): Lost connection to MySQL server during query")
		},
	}

	cfg := regressionConfig(t, p)
	cfg.Insert = r.InsertBatchSnapshot
	cfg.ExtraCheck = r.SkipExtra
	cfg.Repairer = faultRep
	cfg.ScanPageSize, cfg.BatchSize, cfg.TxBatchSize = 10, 10, 10

	rep, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run failed recovering from lost COMMIT ack: %v", err)
	}
	// Attempt 1 committed [a,b] then returned 2013 (Retries=1); Attempt 2 hit real MySQL 1062
	// -> ErrConflict -> fell back to recheck FOR UPDATE -> saw `a` Different (Updated=1) and `b` Stale (Stale=1).
	if rep.Retries != 1 || rep.Updated != 1 || rep.Stale != 1 {
		t.Fatalf("unexpected recovery report: %+v", rep)
	}
	got, err := p.New().Lookup(context.Background(), []store.Key{store.KeyOf(a), store.KeyOf(b)})
	if err != nil {
		t.Fatal(err)
	}
	if got[store.KeyOf(a)].Remark != "fresh-after-ack-loss" || got[store.KeyOf(a)].Version != 2 || got[store.KeyOf(b)] != b {
		t.Fatalf("new rows after lost-ack recovery: %+v", got)
	}
}

func TestRealMySQLLockWaitTimeoutRetriedAndConverges(t *testing.T) {
	dsn := os.Getenv("E2E_MYSQL_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:3306)/reconcile_e2e"
	}
	_ = openDB(t) // ensure tables exist and are clean
	a := order(1, 1)
	stale := order(1, 1)
	stale.Remark = "stale"

	// Open a DB with innodb_lock_wait_timeout=1 session parameter so real InnoDB
	// lock wait timeout (Error 1205) fires within 1s under 20ms latency.
	mcfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	mcfg.DBName = "reconcile_it"
	if mcfg.Params == nil {
		mcfg.Params = map[string]string{}
	}
	mcfg.Params["innodb_lock_wait_timeout"] = "1"
	shortDB, err := sql.Open("mysql", mcfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer shortDB.Close()

	insert(t, shortDB, "it_old", a)
	insert(t, shortDB, newTable(1), stale)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	online, err := shortDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer online.Rollback()
	var id int64
	if err := online.QueryRowContext(ctx, "SELECT order_id FROM it_old WHERE order_id=? FOR UPDATE", a.OrderID).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// Release the online lock as soon as the first real MySQL 1205 lock wait
	// timeout is observed by Retryable, avoiding any wall-clock sleep race.
	var sawTimeout bool
	p := newPair(t, shortDB)
	cfg := regressionConfig(t, p)
	cfg.ExtraCheck = r.SkipExtra
	cfg.Retry = r.Retry{
		MaxAttempts: 3,
		Backoff:     20 * time.Millisecond,
		MaxBackoff:  100 * time.Millisecond,
		Retryable: func(err error) bool {
			tr := mysqltable.IsTransient(err)
			if tr && !sawTimeout {
				sawTimeout = true
				_ = online.Rollback()
			}
			return tr
		},
	}
	rep, err := r.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("expected Run to recover from Error 1205 lock wait timeout, got: %v", err)
	}
	if !sawTimeout || rep.Retries != 1 || rep.Updated != 1 {
		t.Fatalf("expected 1 retry on 1205 and 1 update, got sawTimeout=%v rep=%+v", sawTimeout, rep)
	}
	got, err := p.New().Lookup(ctx, []store.Key{store.KeyOf(a)})
	if err != nil || got[store.KeyOf(a)] != a {
		t.Fatalf("row not repaired after lock wait timeout retry: %+v err=%v", got, err)
	}
}

func TestMidRunTimeoutKillAndRerunIdempotent(t *testing.T) {
	db := openDB(t)
	m0 := order(1, 0) // Missing in shard 0
	m1 := order(2, 1) // Missing in shard 1
	d2 := order(3, 2) // Different in shard 2 (rechecked before missing candidates)
	stale2 := d2
	stale2.Remark = "stale"
	e3 := order(99, 3) // Extra in shard 3

	insert(t, db, "it_old", m0, m1, d2)
	insert(t, db, newTable(2), stale2)
	insert(t, db, newTable(3), e3)

	p := newPair(t, db)
	baseRep := p.Repairer()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	// Cancel ctx1 immediately after the first shard transaction (d2 update) commits on MySQL.
	killRep := &postCommitFaultRepairer{
		Repairer: baseRep,
		g:        baseRep.(r.Grouper[store.Key]),
		onFirst: func() error {
			cancel1()
			return nil
		},
	}

	cfg := regressionConfig(t, p)
	cfg.ScanPageSize, cfg.BatchSize, cfg.TxBatchSize = 10, 10, 10
	cfg.Repairer = killRep
	rep1, err := r.Run(ctx1, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on killed first run, got rep=%+v err=%v", rep1, err)
	}
	if rep1.Updated != 1 || rep1.Inserted != 0 {
		t.Fatalf("expected first shard transaction (d2 update) to remain committed, got %+v", rep1)
	}

	// Second Run with fresh context must see d2 already consistent, repair m0 and m1, and preserve & warn e3.
	cfg.Repairer = baseRep
	rep2, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if rep2.Inserted != 2 || rep2.Updated != 0 || rep2.Different != 0 || rep2.Extra != 1 || rep2.Warned != 1 {
		t.Fatalf("unexpected second Run report: %+v", rep2)
	}

	// Third Run must confirm 0 missing/different and still preserve Extra without deleting it.
	rep3, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("third Run failed: %v", err)
	}
	if rep3.Missing != 0 || rep3.Different != 0 || rep3.Inserted != 0 || rep3.Updated != 0 || rep3.Extra != 1 {
		t.Fatalf("unexpected third Run report: %+v", rep3)
	}
	got, err := p.New().Lookup(context.Background(), []store.Key{store.KeyOf(m0), store.KeyOf(m1), store.KeyOf(d2), store.KeyOf(e3)})
	if err != nil || len(got) != 4 || got[store.KeyOf(m0)] != m0 || got[store.KeyOf(m1)] != m1 || got[store.KeyOf(d2)] != d2 || got[store.KeyOf(e3)] != e3 {
		t.Fatalf("unexpected final rows in New: %+v err=%v", got, err)
	}
}
