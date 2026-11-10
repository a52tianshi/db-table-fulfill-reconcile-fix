package mysqltable

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

type queryScript func(string, []driver.NamedValue) ([][]driver.Value, error)
type scriptDriver struct{ query queryScript }

func (d scriptDriver) Open(string) (driver.Conn, error) { return scriptConn{d.query}, nil }

type scriptConn struct{ query queryScript }

func (scriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not supported by script")
}

func (scriptConn) Close() error { return nil }
func (scriptConn) Begin() (driver.Tx, error) {
	return scriptTx{}, nil
}
func (scriptConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return scriptTx{}, nil
}

type scriptTx struct{}

func (scriptTx) Commit() error   { return nil }
func (scriptTx) Rollback() error { return nil }

func (c scriptConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.query(q, args)
	return &scriptRows{rows: rows}, err
}

func (c scriptConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	_, err := c.query(q, args)
	return driver.RowsAffected(1), err
}

type scriptRows struct {
	rows [][]driver.Value
	pos  int
}

func (*scriptRows) Columns() []string { return []string{"order_id", "user_id"} }
func (*scriptRows) Close() error      { return nil }
func (r *scriptRows) Next(v []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(v, r.rows[r.pos])
	r.pos++
	return nil
}

var scriptID atomic.Int64

func scriptPair(t *testing.T, query queryScript) *Pair[order, key] {
	t.Helper()
	name := fmt.Sprintf("reconcile-script-%d", scriptID.Add(1))
	sql.Register(name, scriptDriver{query})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := pairConfig()
	cfg.DB = db
	cfg.InChunk = 1
	cfg.Schema.Scan = func(scan func(...any) error) (order, error) {
		var o order
		err := scan(&o.ID, &o.UserID)
		return o, err
	}
	p, err := NewPair(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLockSortsBeforeINChunking(t *testing.T) {
	var got []int64
	p := scriptPair(t, func(q string, args []driver.NamedValue) ([][]driver.Value, error) {
		if !strings.Contains(q, "FOR UPDATE") {
			t.Fatalf("not locking SQL: %s", q)
		}
		got = append(got, args[0].Value.(int64))
		return nil, nil
	})
	keys := []key{{ID: 10, UserID: 1}, {ID: 2, UserID: 1}, {ID: 1, UserID: 1}}
	before := append([]key(nil), keys...)
	if _, err := p.fetch(context.Background(), p.cfg.DB, p.cfg.Old, keys, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2, 10}) {
		t.Fatalf("global chunk lock order=%v, want numeric primary-key order [1 2 10]", got)
	}
	if !reflect.DeepEqual(keys, before) {
		t.Fatalf("must not mutate caller keys: %v", keys)
	}
}

func TestScanRejectsWrongShardHighWaterRow(t *testing.T) {
	p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(7), int64(1)}}, nil
	})
	_, _, err := p.New().ScanPage(context.Background(), nil, 3)
	if !errors.Is(err, ErrMisrouted) || !strings.Contains(err.Error(), "orders_new_00") {
		t.Fatalf("expected wrong physical shard error, got %v", err)
	}
}

func TestScanRejectsWrongShardBelowHighWater(t *testing.T) {
	p := scriptPair(t, func(q string, _ []driver.NamedValue) ([][]driver.Value, error) {
		if strings.Contains(q, "DESC LIMIT 1") {
			return [][]driver.Value{{int64(10), int64(0)}}, nil
		}
		return [][]driver.Value{{int64(7), int64(1)}}, nil
	})
	_, _, err := p.New().ScanPage(context.Background(), nil, 3)
	if !errors.Is(err, ErrMisrouted) {
		t.Fatal("wrong-shard page row must be rejected, not returned as a valid scan")
	}
}

func TestLookupRejectsWrongShardRow(t *testing.T) {
	p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(7), int64(1)}}, nil
	})
	_, err := p.New().Lookup(context.Background(), []key{{ID: 7, UserID: 0}})
	if !errors.Is(err, ErrMisrouted) {
		t.Fatal("wrong-shard fetched row must be rejected")
	}
}

func TestMisroutedErrorDetailsAndClassification(t *testing.T) {
	p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(7), int64(1)}}, nil
	})
	_, _, err := p.New().ScanPage(context.Background(), nil, 3)
	var detail *MisroutedError
	if !errors.As(err, &detail) || detail.Table != "orders_new_00" || detail.Expected != "orders_new_01" || detail.Key != (key{7, 1}) {
		t.Fatalf("%#v: %v", detail, err)
	}
	// A row's printed key must not trick string-based transient detection.
	if IsTransient(fmt.Errorf("context: %w", &MisroutedError{Key: "Error 1213: invalid connection", Table: "a", Expected: "b"})) {
		t.Fatal("routing errors must not be classified transient")
	}
}

func TestLockSortsMultipleKeysPerChunk(t *testing.T) {
	var got [][]int64
	p := scriptPair(t, func(_ string, args []driver.NamedValue) ([][]driver.Value, error) {
		chunk := make([]int64, len(args))
		for i, a := range args {
			chunk[i] = a.Value.(int64)
		}
		got = append(got, chunk)
		return nil, nil
	})
	p.cfg.InChunk = 2
	if _, err := p.fetch(context.Background(), p.cfg.DB, p.cfg.Old, []key{{10, 1}, {2, 1}, {9, 1}, {1, 1}}, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]int64{{1, 2}, {9, 10}}) {
		t.Fatalf("%v", got)
	}
}

func TestOldSideScanAlsoRejectsWrongShard(t *testing.T) {
	p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(7), int64(1)}}, nil
	})
	p.cfg.Old = Mod("orders_old_%02d", 4, func(k key) int64 { return k.UserID })
	if _, _, err := p.Old().ScanPage(context.Background(), nil, 3); !errors.Is(err, ErrMisrouted) {
		t.Fatalf("%v", err)
	}
}

// Ported from the codex self-test branch: the first row is short and the
// second row is long, so the total argument count (1+3 = 2 rows x 2 columns)
// matches the multi-row INSERT placeholders and database/sql cannot catch it.
// Without per-row validation this executes and writes shifted columns.
func TestTxInsertRejectsCompensatingValuesLengths(t *testing.T) {
	ctx := context.Background()
	var executed []string
	p := scriptPair(t, func(q string, _ []driver.NamedValue) ([][]driver.Value, error) {
		executed = append(executed, q)
		return nil, nil
	})
	p.cfg.InsertChunk = 2
	p.cfg.Schema.Values = func(o order) []any {
		if o.ID == 1 {
			return []any{o.ID} // short: 1 value for 2 columns
		}
		return []any{o.ID, o.UserID, int64(9)} // long: 3 values for 2 columns
	}
	err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
		return tx.Insert(ctx, []order{{ID: 1, UserID: 1}, {ID: 2, UserID: 1}})
	})
	if !errors.Is(err, ErrInvalidValues) || IsTransient(err) {
		t.Fatalf("per-row Values length mismatch must fail permanently with ErrInvalidValues: %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("malformed rows must not execute SQL even if total args match: %q", executed)
	}
}

func TestTxUpdateAndInsertRejectWrongValuesLength(t *testing.T) {
	ctx := context.Background()
	t.Run("update too few values returns error without panic", func(t *testing.T) {
		var executed int
		p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
			executed++
			return nil, nil
		})
		p.cfg.Schema.Values = func(o order) []any { return []any{o.ID} } // 1 value for 2 columns
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Update panicked instead of returning error: %v", r)
			}
		}()
		err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
			return tx.Update(ctx, order{ID: 1, UserID: 2})
		})
		if err == nil {
			t.Fatal("expected error when Values returns fewer items than Columns")
		}
		if !errors.Is(err, ErrInvalidValues) || IsTransient(err) {
			t.Fatalf("want permanent ErrInvalidValues, got %v", err)
		}
		if executed != 0 {
			t.Fatalf("expected 0 SQL executions, got %d", executed)
		}
	})

	t.Run("update too many values returns error instead of silent truncation", func(t *testing.T) {
		var executed int
		p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
			executed++
			return nil, nil
		})
		p.cfg.Schema.Values = func(o order) []any { return []any{o.ID, o.UserID, "extra"} } // 3 values for 2 columns
		err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
			return tx.Update(ctx, order{ID: 1, UserID: 2})
		})
		if err == nil {
			t.Fatal("expected error when Values returns more items than Columns")
		}
		if !errors.Is(err, ErrInvalidValues) || IsTransient(err) {
			t.Fatalf("want permanent ErrInvalidValues, got %v", err)
		}
		if executed != 0 {
			t.Fatalf("expected 0 SQL executions, got %d", executed)
		}
	})

	t.Run("insert rejects mismatched values before multi-row shift", func(t *testing.T) {
		var executed int
		p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
			executed++
			return nil, nil
		})
		p.cfg.InsertChunk = 2
		// Row 1 returns 3 values, Row 2 returns 1 value -> total 4 args for 2x2 placeholders!
		p.cfg.Schema.Values = func(o order) []any {
			if o.ID == 1 {
				return []any{o.ID, o.UserID, int64(999)}
			}
			return []any{o.UserID}
		}
		err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
			return tx.Insert(ctx, []order{{ID: 1, UserID: 1}, {ID: 2, UserID: 1}})
		})
		if err == nil {
			t.Fatal("expected error when Values length mismatches Columns in Insert")
		}
		if !errors.Is(err, ErrInvalidValues) || IsTransient(err) {
			t.Fatalf("want permanent ErrInvalidValues, got %v", err)
		}
		if executed != 0 {
			t.Fatalf("expected 0 SQL executions, got %d", executed)
		}
	})
}

func TestInsertSortsByKeyLessBeforeChunking(t *testing.T) {
	ctx := context.Background()
	var chunks [][]int64
	p := scriptPair(t, func(q string, args []driver.NamedValue) ([][]driver.Value, error) {
		if !strings.HasPrefix(q, "INSERT INTO") {
			t.Fatalf("unexpected query: %s", q)
		}
		var ids []int64
		for i := 0; i < len(args); i += 2 {
			ids = append(ids, args[i].Value.(int64))
		}
		chunks = append(chunks, ids)
		return nil, nil
	})
	p.cfg.InsertChunk = 2
	rows := []order{{ID: 10, UserID: 1}, {ID: 2, UserID: 1}, {ID: 9, UserID: 1}, {ID: 1, UserID: 1}}
	before := append([]order(nil), rows...)
	err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
		return tx.Insert(ctx, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int64{{1, 2}, {9, 10}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("Insert key order across chunks = %v, want %v", chunks, want)
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatalf("Insert must not mutate caller slice: %v", rows)
	}
}

func TestRouteOutsideTablesRejectedAcrossOperations(t *testing.T) {
	ctx := context.Background()
	var queries int
	p := scriptPair(t, func(string, []driver.NamedValue) ([][]driver.Value, error) {
		queries++
		return nil, nil
	})
	p.cfg.New = RouterFunc[key]([]string{"orders_new_00", "orders_new_01"}, func(k key) string {
		return "orders_new_99" // not in Tables()
	})
	if _, err := p.New().Lookup(ctx, []key{{ID: 1, UserID: 1}}); !errors.Is(err, ErrMisrouted) {
		t.Fatalf("Lookup: want ErrMisrouted, got %v", err)
	}
	err := p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
		_, _, err := tx.Lock(ctx, []key{{ID: 1, UserID: 1}})
		return err
	})
	if !errors.Is(err, ErrMisrouted) {
		t.Fatalf("Lock: want ErrMisrouted, got %v", err)
	}
	err = p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
		return tx.Insert(ctx, []order{{ID: 1, UserID: 1}})
	})
	if !errors.Is(err, ErrMisrouted) {
		t.Fatalf("Insert: want ErrMisrouted, got %v", err)
	}
	err = p.Repairer().InTx(ctx, func(tx reconcile.Tx[order, key]) error {
		return tx.Update(ctx, order{ID: 1, UserID: 1})
	})
	if !errors.Is(err, ErrMisrouted) {
		t.Fatalf("Update: want ErrMisrouted, got %v", err)
	}
	// Old.Lookup consumes 1 query in Lock before New.Lock fails; New must execute 0 queries.
	if queries != 1 {
		t.Fatalf("expected only Old lock query before New route check failed, got %d queries", queries)
	}
}

func TestCrossSideCursorAndZeroLimitRejected(t *testing.T) {
	ctx := context.Background()
	p := scriptPair(t, func(q string, args []driver.NamedValue) ([][]driver.Value, error) {
		if strings.Contains(q, "DESC LIMIT 1") {
			return [][]driver.Value{{int64(10), int64(0)}}, nil
		}
		if len(args) > 0 && args[len(args)-1].Value == int64(0) {
			return nil, nil // SELECT ... LIMIT 0 returns 0 rows
		}
		return [][]driver.Value{{int64(1), int64(0)}}, nil
	})
	t.Run("zero limit returns error without panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ScanPage(limit=0) panicked: %v", r)
			}
		}()
		if _, _, err := p.Old().ScanPage(ctx, nil, 0); err == nil {
			t.Fatal("ScanPage with limit=0 must return error, not nil")
		}
	})
	t.Run("cursor from New rejected by Old", func(t *testing.T) {
		_, cur, err := p.New().ScanPage(ctx, nil, 2)
		if err != nil || cur == nil {
			t.Fatalf("New.ScanPage: cur=%v err=%v", cur, err)
		}
		// Passing a cursor from New (table orders_new_01) to Old (table orders_old) must fail.
		if _, _, err := p.Old().ScanPage(ctx, cur, 2); err == nil {
			t.Fatal("expected foreign cursor error when passing New cursor to Old.ScanPage")
		}
	})
}
