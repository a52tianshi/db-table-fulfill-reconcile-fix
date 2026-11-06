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

	pair, err := mysqltable.NewPair(db, schema, oldTables, newTables, mysqltable.Options{
		PageSize: 1000, // rows per scan query
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

	// 4. Engine: what to do, how big the batches are.
	engine, err := r.New(r.Config[Order, OrderKey]{
		Old:      pair.Old(),
		New:      pair.New(),
		Key:      schema.Key,
		Compare:  compare,
		Repairer: pair.Repairer(),
		Notifier: r.LogNotifier[Order, OrderKey]{W: os.Stderr},
		Policy:   r.Policy{Missing: r.Fix, Different: r.Warn},
		Insert:   r.InsertBatchLocked,

		BatchSize:   1000,
		TxBatchSize: 200,
		MemoryLimit: 64 << 20,
		RowSize:     func(o Order) int64 { return int64(64 + len(o.Remark)) },
	})
	if err != nil {
		log.Fatal(err)
	}

	rep, err := engine.Run(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	log.Println(rep, "consistent:", rep.Consistent())
}
