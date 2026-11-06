// Package store holds the e2e MySQL schema, snowflake ids and the mapping
// onto the library's mysqltable adapter. Old table: orders_old. New tables: orders_new_00
// .. orders_new_99, routed by user_id % 100. order_id / user_id are immutable.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
	"github.com/go-sql-driver/mysql"
)

const (
	Shards   = 100
	OldTable = "orders_old"
	Cols     = "order_id,user_id,status,amount,remark,version,updated_at"
)

func ShardTable(i int) string      { return fmt.Sprintf("orders_new_%02d", i) }
func NewTable(userID int64) string { return ShardTable(int(userID % Shards)) }
func Placeholders(n int) string    { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
func TxOpts() *sql.TxOptions       { return &sql.TxOptions{Isolation: sql.LevelReadCommitted} }
func KeyOf(o Order) Key            { return Key{OrderID: o.OrderID, UserID: o.UserID} }
func (o Order) Args() []any {
	return []any{o.OrderID, o.UserID, o.Status, o.Amount, o.Remark, o.Version, o.UpdatedAt}
}
func (o Order) MutableArgs() []any {
	return []any{o.Status, o.Amount, o.Remark, o.Version, o.UpdatedAt, o.OrderID}
}
func (k Key) String() string { return fmt.Sprintf("%d/u%d", k.OrderID, k.UserID) }

// Order: identical structure in old and new tables. updated_at is unix micros
// set by the application, so both sides get exactly the same value.
type Order struct {
	OrderID, UserID int64
	Status          int32
	Amount          int64
	Remark          string
	Version         int64
	UpdatedAt       int64
}

// Key carries user_id so new-table lookups can be routed to the right shard.
type Key struct{ OrderID, UserID int64 }

const UpdateSet = "status=?,amount=?,remark=?,version=?,updated_at=? WHERE order_id=?"

type scanner interface{ Scan(...any) error }

func ScanOrder(s scanner) (Order, error) {
	var o Order
	err := s.Scan(&o.OrderID, &o.UserID, &o.Status, &o.Amount, &o.Remark, &o.Version, &o.UpdatedAt)
	return o, err
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func QueryOrders(ctx context.Context, q querier, query string, args ...any) ([]Order, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := ScanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func CreateSchema(ctx context.Context, db *sql.DB) error {
	tables := []string{OldTable}
	for i := 0; i < Shards; i++ {
		tables = append(tables, ShardTable(i))
	}
	for _, t := range tables {
		ddl := []string{
			"DROP TABLE IF EXISTS " + t,
			"CREATE TABLE " + t + ` (
  order_id   BIGINT      NOT NULL PRIMARY KEY,
  user_id    BIGINT      NOT NULL,
  status     INT         NOT NULL,
  amount     BIGINT      NOT NULL,
  remark     VARCHAR(64) NOT NULL,
  version    BIGINT      NOT NULL,
  updated_at BIGINT      NOT NULL,
  KEY idx_user (user_id)
) ENGINE=InnoDB`,
		}
		for _, s := range ddl {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
		}
	}
	return nil
}

// Retryable reports InnoDB deadlock / lock wait timeout.
func Retryable(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205)
}

// IsDuplicate reports a duplicate key error.
func IsDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// Snowflake: 41 bit ms since epoch | 10 bit worker | 12 bit sequence.
// Ids from different workers are only roughly increasing, like production.
type Snowflake struct {
	mu          sync.Mutex
	Worker      int64
	lastMs, seq int64
}

const snowEpoch = 1700000000000

func (s *Snowflake) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := time.Now().UnixMilli()
	if ms < s.lastMs {
		ms = s.lastMs
	}
	if ms == s.lastMs {
		s.seq = (s.seq + 1) & 4095
		if s.seq == 0 {
			for ms <= s.lastMs {
				ms = time.Now().UnixMilli()
			}
		}
	} else {
		s.seq = 0
	}
	s.lastMs = ms
	return (ms-snowEpoch)<<22 | (s.Worker&1023)<<12 | s.seq
}

// Schema maps Order onto the library's MySQL adapter.
func Schema() mysqltable.Schema[Order, Key] {
	return mysqltable.Schema[Order, Key]{
		Columns:   strings.Split(Cols, ","),
		KeyColumn: "order_id",
		Key:       KeyOf,
		KeyValue:  func(k Key) any { return k.OrderID },
		Values:    Order.Args,
		Scan: func(scan func(...any) error) (Order, error) {
			return ScanOrder(scanFunc(scan))
		},
	}
}

type scanFunc func(...any) error

func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

// Routers: one old table; new tables sharded by user_id % 100.
func OldRouter() mysqltable.Router[Key] { return mysqltable.Single[Key](OldTable) }
func NewRouter() mysqltable.Router[Key] {
	return mysqltable.Mod("orders_new_%02d", Shards, func(k Key) int64 { return k.UserID })
}
