package mysqltable_test

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
)

// Order is your row type: same columns in the old table and every new shard.
type Order struct {
	OrderID   int64
	UserID    int64
	Status    int32
	Amount    int64
	Remark    string
	UpdatedAt time.Time
}

// OrderKey identifies a row. UserID is included because the new tables are
// sharded by it; both fields are immutable.
type OrderKey struct{ OrderID, UserID int64 }

func Example() {
	var db *sql.DB // sql.Open("mysql", dsn) with your driver

	// 1. Columns <-> struct (order of Columns = order of Values and Scan).
	schema := mysqltable.Schema[Order, OrderKey]{
		Columns:   []string{"order_id", "user_id", "status", "amount", "remark", "updated_at"},
		KeyColumn: "order_id",
		Key:       func(o Order) OrderKey { return OrderKey{o.OrderID, o.UserID} },
		KeyValue:  func(k OrderKey) any { return k.OrderID },
		Values: func(o Order) []any {
			return []any{o.OrderID, o.UserID, o.Status, o.Amount, o.Remark, o.UpdatedAt}
		},
		Scan: func(scan func(...any) error) (Order, error) {
			var o Order
			err := scan(&o.OrderID, &o.UserID, &o.Status, &o.Amount, &o.Remark, &o.UpdatedAt)
			return o, err
		},
	}

	// 2. Sharding rules: old is one table, new is orders_new_00..99 by user_id % 100.
	oldTables := mysqltable.Single[OrderKey]("orders_old")
	newTables := mysqltable.Mod("orders_new_%02d", 100, func(k OrderKey) int64 { return k.UserID })

	pair, err := mysqltable.NewPair(mysqltable.PairConfig[Order, OrderKey]{
		DB:          db,
		Schema:      schema,
		Old:         oldTables,
		New:         newTables,
		TxOptions:   &sql.TxOptions{Isolation: sql.LevelReadCommitted},
		InChunk:     1000, // keys per IN (...) query
		InsertChunk: 500,  // rows per multi-row INSERT
		IsDuplicate: mysqltable.IsDuplicateKey,
	})
	if err != nil {
		log.Fatal(err)
	}

	// 3. Which fields must match: everything except Remark; UpdatedAt may
	//    differ by up to 1s.
	compare, err := r.Struct([]string{"Remark"},
		r.Within("UpdatedAt", func(o Order) int64 { return o.UpdatedAt.UnixMilli() }, 1000))
	if err != nil {
		log.Fatal(err)
	}

	// 4. Run: every field is required; Run validates before touching data.
	rep, err := r.Run(context.Background(), r.Config[Order, OrderKey]{
		Old:        pair.Old(),
		New:        pair.New(),
		Key:        schema.Key,
		Compare:    compare,
		Policy:     r.Policy{Missing: r.Fix, Different: r.Warn},
		Insert:     r.InsertBatchLocked,
		ExtraCheck: r.CheckExtra,
		Repairer:   pair.Repairer(),
		Notifier:   r.LogNotifier[Order, OrderKey]{W: os.Stderr},
		Progress:   r.LogProgress(os.Stderr, 10*time.Second), // or r.NoProgress

		ScanPageSize: 1000,     // rows per scan query
		BatchSize:    1000,     // rows paired per lookup of the other side
		TxBatchSize:  200,      // keys per recheck / insert transaction
		MemoryLimit:  64 << 20, // bytes buffered (or reconcile.NoMemoryLimit)
		RowSize:      func(o Order) int64 { return int64(64 + len(o.Remark)) },

		Retry: r.Retry{ // every SQL step; exhausted -> Run returns the error
			MaxAttempts: 5,
			Backoff:     50 * time.Millisecond,
			MaxBackoff:  2 * time.Second,
			Retryable:   mysqltable.IsTransient,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(rep, "consistent:", rep.Consistent())
}
