package mysqltable

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

type order struct{ ID, UserID int64 }
type key struct{ ID, UserID int64 }

func testSchema() Schema[order, key] {
	return Schema[order, key]{
		Columns:   []string{"order_id", "user_id"},
		KeyColumn: "order_id",
		Key:       func(o order) key { return key(o) },
		KeyValue:  func(k key) any { return k.ID },
		KeyLess:   func(a, b key) bool { return a.ID < b.ID },
		Values:    func(o order) []any { return []any{o.ID, o.UserID} },
		Scan: func(scan func(...any) error) (order, error) {
			var o order
			return o, scan(&o.ID, &o.UserID)
		},
	}
}

func TestModRouter(t *testing.T) {
	r := Mod("orders_new_%02d", 100, func(k key) int64 { return k.UserID })
	if n := len(r.Tables()); n != 100 || r.Tables()[7] != "orders_new_07" {
		t.Fatal(r.Tables()[:3])
	}
	if got := r.Route(key{1, 12345}); got != "orders_new_45" {
		t.Fatal(got)
	}
	if got := r.Route(key{1, -3}); got != "orders_new_03" {
		t.Fatal(got)
	}
}

func TestQueries(t *testing.T) {
	q := newQueries([]string{"order_id", "user_id", "amount"}, "order_id")
	cases := map[string]string{
		q.last("t"):        "SELECT `order_id`,`user_id`,`amount` FROM `t` ORDER BY `order_id` DESC LIMIT 1",
		q.page("t", true):  "SELECT `order_id`,`user_id`,`amount` FROM `t` WHERE `order_id` <= ? ORDER BY `order_id` LIMIT ?",
		q.page("t", false): "SELECT `order_id`,`user_id`,`amount` FROM `t` WHERE `order_id` > ? AND `order_id` <= ? ORDER BY `order_id` LIMIT ?",
		q.in("t", 2, true): "SELECT `order_id`,`user_id`,`amount` FROM `t` WHERE `order_id` IN (?,?) ORDER BY `order_id` FOR UPDATE",
		q.insert("t", 2):   "INSERT INTO `t` (`order_id`,`user_id`,`amount`) VALUES (?,?,?),(?,?,?)",
		q.update("t"):      "UPDATE `t` SET `user_id`=?,`amount`=? WHERE `order_id` = ?",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("\n got %s\nwant %s", got, want)
		}
	}
}

func TestByTableSortedForLockOrder(t *testing.T) {
	r := Mod("s%d", 3, func(k key) int64 { return k.UserID })
	tables, groups := byTable(r, []key{{1, 2}, {2, 0}, {3, 1}, {4, 2}})
	if len(tables) != 3 || tables[0] != "s0" || tables[2] != "s2" || len(groups["s2"]) != 2 {
		t.Fatal(tables, groups)
	}
}

func pairConfig() PairConfig[order, key] {
	return PairConfig[order, key]{
		DB:          &sql.DB{}, // never used: NewPair does no I/O
		Schema:      testSchema(),
		Old:         Single[key]("orders_old"),
		New:         Mod("orders_new_%02d", 4, func(k key) int64 { return k.UserID }),
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     1000,
		InsertChunk: 500,
		IsDuplicate: IsDuplicateKey,
	}
}

func TestNewPairValidates(t *testing.T) {
	if _, err := NewPair(pairConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPair(PairConfig[order, key]{}); err == nil {
		t.Fatal("empty config must fail")
	}
	cases := map[string]func(*PairConfig[order, key]){
		"key column":   func(c *PairConfig[order, key]) { c.Schema.KeyColumn = "id" },
		"bad table":    func(c *PairConfig[order, key]) { c.Old = Single[key]("bad`name") },
		"no tx opts":   func(c *PairConfig[order, key]) { c.TxOptions = nil },
		"zero chunk":   func(c *PairConfig[order, key]) { c.InChunk = 0 },
		"no dup func":  func(c *PairConfig[order, key]) { c.IsDuplicate = nil },
		"no scan":      func(c *PairConfig[order, key]) { c.Schema.Scan = nil },
		"no key order": func(c *PairConfig[order, key]) { c.Schema.KeyLess = nil },
	}
	for name, mutate := range cases {
		c := pairConfig()
		mutate(&c)
		if _, err := NewPair(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestErrorClassifiers(t *testing.T) {
	transient := []error{
		errors.New("Error 1213 (40001): Deadlock found"),
		errors.New("Error 1205 (HY000): Lock wait timeout exceeded"),
		errors.New("invalid connection"),
		fmt.Errorf("wrapped: %w", driver.ErrBadConn),
	}
	for _, err := range transient {
		if !IsTransient(err) {
			t.Errorf("should retry: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("Error 1062 (23000): Duplicate entry"), errors.New("Error 1146: no table")} {
		if IsTransient(err) {
			t.Errorf("should not retry: %v", err)
		}
	}
	if !IsDuplicateKey(errors.New("Error 1062 (23000): Duplicate entry")) || IsDuplicateKey(nil) {
		t.Fatal("duplicate")
	}
}
