package mysqltable

import (
	"database/sql"
	"errors"
	"testing"
)

type order struct{ ID, UserID int64 }
type key struct{ ID, UserID int64 }

func testSchema() Schema[order, key] {
	return Schema[order, key]{
		Columns:   []string{"order_id", "user_id"},
		KeyColumn: "order_id",
		Key:       func(o order) key { return key{o.ID, o.UserID} },
		KeyValue:  func(k key) any { return k.ID },
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

func TestNewPairValidates(t *testing.T) {
	db := &sql.DB{} // never used: validation fails first or succeeds without I/O
	old, nw := Single[key]("orders_old"), Mod("orders_new_%02d", 4, func(k key) int64 { return k.UserID })
	if _, err := NewPair(db, testSchema(), old, nw, Options{}); err != nil {
		t.Fatal(err)
	}
	bad := testSchema()
	bad.KeyColumn = "id"
	if _, err := NewPair(db, bad, old, nw, Options{}); err == nil {
		t.Fatal("KeyColumn not in Columns must fail")
	}
	if _, err := NewPair(db, testSchema(), Single[key]("bad`name"), nw, Options{}); err == nil {
		t.Fatal("backtick in table name must fail")
	}
}

func TestDefaultErrorClassifiers(t *testing.T) {
	var o Options
	o.normalize()
	if !o.IsRetryable(errors.New("Error 1213 (40001): Deadlock found")) || o.IsRetryable(errors.New("Error 1062")) {
		t.Fatal("retryable")
	}
	if !o.IsDuplicate(errors.New("Error 1062 (23000): Duplicate entry")) || o.PageSize != 1000 {
		t.Fatal("duplicate / defaults")
	}
}
