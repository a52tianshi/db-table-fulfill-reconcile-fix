package reconcile_test

import (
	"context"
	"fmt"
	"os"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

func Example() {
	type Order struct {
		ID     int
		Amount int
		Remark string // allowed to differ
	}
	store := memtable.New(func(o Order) int { return o.ID },
		[]Order{{1, 100, "a"}, {2, 200, "b"}, {3, 300, "c"}},  // old (authoritative)
		[]Order{{1, 100, "a2"}, {2, 999, "b"}, {4, 400, "d"}}, // new
	)
	compare, err := r.Struct[Order]([]string{"Remark"})
	if err != nil {
		panic(err)
	}
	engine, err := r.New(r.Config[Order, int]{
		Old:      store.Old(),
		New:      store.New(),
		Key:      func(o Order) int { return o.ID },
		Compare:  compare,
		Repairer: store,
		Notifier: r.LogNotifier[Order, int]{W: os.Stdout},
		Policy:   r.Policy{Missing: r.Fix, Different: r.Fix},
	})
	if err != nil {
		panic(err)
	}
	rep, err := engine.Run(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(rep.Missing, rep.Different, rep.Extra, rep.Inserted, rep.Updated)
	fmt.Println(store.Rows(false))
	// Output:
	// reconcile WARN extra key=4 fields=[]
	// 1 1 1 1 1
	// [{1 100 a2} {2 200 b} {3 300 c} {4 400 d}]
}
