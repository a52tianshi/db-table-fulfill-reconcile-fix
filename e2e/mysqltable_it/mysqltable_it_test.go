// Package mysqltable_it tests the library's MySQL adapter against a real
// MySQL (DSN from E2E_MYSQL_DSN, database name replaced by reconcile_it).
// Skipped when MySQL is unreachable.
package mysqltable_it

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
	"github.com/go-sql-driver/mysql"
)

const shards = 4

func newTable(i int) string { return fmt.Sprintf("it_new_%d", i) }

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("E2E_MYSQL_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:3306)/reconcile_e2e"
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("MySQL unreachable: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE IF NOT EXISTS reconcile_it"); err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "reconcile_it"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tables := []string{"it_old"}
	for i := 0; i < shards; i++ {
		tables = append(tables, newTable(i))
	}
	for _, tb := range tables {
		for _, q := range []string{"DROP TABLE IF EXISTS " + tb, "CREATE TABLE " + tb + ` (
  order_id BIGINT NOT NULL PRIMARY KEY, user_id BIGINT NOT NULL, status INT NOT NULL,
  amount BIGINT NOT NULL, remark VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
  updated_at BIGINT NOT NULL) ENGINE=InnoDB`} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func newPair(t *testing.T, db *sql.DB) *mysqltable.Pair[store.Order, store.Key] {
	t.Helper()
	p, err := mysqltable.NewPair(mysqltable.PairConfig[store.Order, store.Key]{
		DB:          db,
		Schema:      store.Schema(),
		Old:         mysqltable.Single[store.Key]("it_old"),
		New:         mysqltable.Mod("it_new_%d", shards, func(k store.Key) int64 { return k.UserID }),
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     3, // tiny on purpose: lookups and locks span several IN queries
		InsertChunk: 2,
		IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// big ids exercise the typed high-water mark (snowflake-sized BIGINTs lose
// precision if compared as DOUBLE).
const base = int64(1) << 62

func order(id, user int64) store.Order {
	return store.Order{OrderID: base + id, UserID: user, Status: 1, Amount: id, Remark: "r", Version: 1, UpdatedAt: id}
}

func insert(t *testing.T, db *sql.DB, table string, rows ...store.Order) {
	t.Helper()
	for _, o := range rows {
		if _, err := db.Exec("INSERT INTO "+table+" ("+store.Cols+") VALUES (?,?,?,?,?,?,?)", o.Args()...); err != nil {
			t.Fatal(err)
		}
	}
}

func scanAll(t *testing.T, src r.Source[store.Order, store.Key], limit int) (keys []int64, pages int) {
	t.Helper()
	ctx := context.Background()
	var cur r.Cursor
	for {
		rows, next, err := src.ScanPage(ctx, cur, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > limit {
			t.Fatalf("page of %d rows > limit %d", len(rows), limit)
		}
		// Retrying the same cursor must return the same page.
		again, next2, err := src.ScanPage(ctx, cur, limit)
		if err != nil || len(again) != len(rows) || (next == nil) != (next2 == nil) {
			t.Fatalf("retry of same cursor differs: %d vs %d rows", len(rows), len(again))
		}
		pages++
		for _, o := range rows {
			keys = append(keys, o.OrderID-base)
		}
		if next == nil {
			return keys, pages
		}
		cur = next
	}
}

// Shard sizes around the page size: 0, 1, limit, limit+1.
func TestScanPageUnevenShards(t *testing.T) {
	db := openDB(t)
	const limit = 3
	want := []int64{}
	id := int64(0)
	for shard, n := range []int{0, 1, limit, limit + 1} {
		for i := 0; i < n; i++ {
			id++
			insert(t, db, newTable(shard), order(id, int64(shard)))
			want = append(want, id)
		}
	}
	keys, pages := scanAll(t, newPair(t, db).New(), limit)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("keys %v want %v", keys, want)
	}
	// shard1: 1 page, shard2: full page + empty page, shard3: 2 pages
	if pages != 5 {
		t.Fatalf("pages=%d", pages)
	}
	if keys, _ := scanAll(t, newPair(t, db).Old(), limit); len(keys) != 0 {
		t.Fatalf("empty old table yielded %v", keys)
	}
}

func TestScanStopsAtHighWaterMark(t *testing.T) {
	db := openDB(t)
	insert(t, db, "it_old", order(1, 0), order(2, 0), order(3, 0))
	src := newPair(t, db).Old()
	rows, next, err := src.ScanPage(context.Background(), nil, 2)
	if err != nil || len(rows) != 2 || next == nil {
		t.Fatal(rows, next, err)
	}
	insert(t, db, "it_old", order(4, 0)) // after this table's scan started
	rows, next, err = src.ScanPage(context.Background(), next, 2)
	if err != nil || len(rows) != 1 || rows[0].OrderID != base+3 || next != nil {
		t.Fatalf("%v next=%v err=%v", rows, next, err)
	}
}

func TestLookupRoutesAndConfirmsAbsence(t *testing.T) {
	db := openDB(t)
	insert(t, db, newTable(1), order(1, 1), order(5, 5))
	insert(t, db, newTable(2), order(2, 2))
	got, err := newPair(t, db).New().Lookup(context.Background(), []store.Key{
		{OrderID: base + 1, UserID: 1}, {OrderID: base + 2, UserID: 2}, {OrderID: base + 5, UserID: 5},
		{OrderID: base + 9, UserID: 1}, // absent
	})
	if err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
}

func TestTxInsertUpdateConflictRollback(t *testing.T) {
	db := openDB(t)
	p := newPair(t, db)
	rep := p.Repairer()
	ctx := context.Background()
	rows := []store.Order{order(1, 0), order(2, 1), order(3, 1), order(4, 2), order(5, 3)}
	insert(t, db, "it_old", rows...)
	keys := make([]store.Key, len(rows))
	for i, o := range rows {
		keys[i] = store.KeyOf(o)
	}
	// Insert across shards, chunked by InsertChunk=2.
	err := rep.InTx(ctx, func(tx r.Tx[store.Order, store.Key]) error {
		old, cur, err := tx.Lock(ctx, keys)
		if err != nil || len(old) != 5 || len(cur) != 0 {
			return fmt.Errorf("lock: old=%d new=%d err=%v", len(old), len(cur), err)
		}
		return tx.Insert(ctx, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate -> ErrConflict, whole tx rolled back.
	err = rep.InTx(ctx, func(tx r.Tx[store.Order, store.Key]) error {
		if err := tx.Update(ctx, store.Order{OrderID: base + 1, UserID: 0, Remark: "rolled-back"}); err != nil {
			return err
		}
		return tx.Insert(ctx, rows[:1])
	})
	if !errors.Is(err, r.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	// Update overwrites every non-key column.
	fresh := order(2, 1)
	fresh.Remark, fresh.Amount, fresh.Version = "fixed", 777, 9
	if err := rep.InTx(ctx, func(tx r.Tx[store.Order, store.Key]) error { return tx.Update(ctx, fresh) }); err != nil {
		t.Fatal(err)
	}
	got, err := p.New().Lookup(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if got[keys[0]].Remark != "r" || got[keys[1]] != fresh || len(got) != 5 {
		t.Fatalf("%+v", got)
	}
}

// Lock must take row locks: a second transaction cannot update a locked row.
func TestLockHoldsRowLocks(t *testing.T) {
	db := openDB(t)
	insert(t, db, "it_old", order(1, 0))
	p := newPair(t, db)
	ctx := context.Background()
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.Repairer().InTx(ctx, func(tx r.Tx[store.Order, store.Key]) error {
			if _, _, err := tx.Lock(ctx, []store.Key{store.KeyOf(order(1, 0))}); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 1")
	_, err = conn.ExecContext(ctx, "UPDATE it_old SET remark='x' WHERE order_id=?", base+1)
	close(release)
	if !mysqltable.IsTransient(err) {
		t.Fatalf("expected lock wait timeout while locked, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Full Run with tiny, mutually prime sizes over uneven shards.
func TestRunConvergesWithOddSizes(t *testing.T) {
	db := openDB(t)
	var old []store.Order
	for i := int64(1); i <= 200; i++ {
		user := i % 3 // shard 3 stays empty
		if i%7 == 0 {
			user = 1
		}
		old = append(old, order(i, user))
	}
	insert(t, db, "it_old", old...)
	stale := order(10, old[9].UserID)
	stale.Remark = "stale"
	insert(t, db, newTable(int(stale.UserID%shards)), stale)
	p := newPair(t, db)
	cmp, err := r.Struct[store.Order](nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := r.Config[store.Order, store.Key]{
		Old: p.Old(), New: p.New(), Key: store.KeyOf, Compare: cmp,
		Policy: r.Policy{Missing: r.Fix, Different: r.Fix}, Insert: r.InsertBatchLocked,
		ExtraCheck: r.CheckExtra, Repairer: p.Repairer(),
		Notifier:     r.LogNotifier[store.Order, store.Key]{W: os.Stderr},
		Progress:     r.NoProgress,
		ScanPageSize: 7, BatchSize: 13, TxBatchSize: 5,
		MemoryLimit: 500, RowSize: func(store.Order) int64 { return 60 },
		Retry: r.Retry{MaxAttempts: 3, Backoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond, Retryable: mysqltable.IsTransient},
	}
	rep, err := r.Run(context.Background(), cfg)
	if err != nil || rep.Inserted != 199 || rep.Updated != 1 || rep.OldScanned != 200 {
		t.Fatalf("%v %v", rep, err)
	}
	rep, err = r.Run(context.Background(), cfg)
	if err != nil || !rep.Consistent() || rep.Inserted+rep.Updated != 0 || rep.NewScanned != 200 {
		t.Fatalf("second run %v %v", rep, err)
	}
}
