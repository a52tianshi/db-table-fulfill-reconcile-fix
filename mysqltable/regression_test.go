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
	return nil, fmt.Errorf("transaction not used by script")
}

func (c scriptConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.query(q, args)
	return &scriptRows{rows: rows}, err
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
