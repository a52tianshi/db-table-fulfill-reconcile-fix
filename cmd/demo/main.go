package main

import (
	"context"
	"fmt"
	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memory"
)

func main() {
	s := &memory.Store{Old: map[int]memory.Row{1: {Identifier: 1, Value: "same"}, 2: {Identifier: 2, Value: "authoritative"}, 3: {Identifier: 3, Value: "insert"}}, New: map[int]memory.Row{1: {Identifier: 1, Value: "same"}, 2: {Identifier: 2, Value: "stale"}, 4: {Identifier: 4, Value: "keep"}}}
	e := r.Engine[memory.Row, int]{Old: memory.Side{Store: s, Old: true}, New: memory.Side{Store: s}, Records: memory.Records{}, Cache: &r.MemoryCache[memory.Row, int]{Limit: 2}, Reviewer: s, BatchSize: 2}
	for _, mode := range []r.Mode{r.AlertOnly, r.Repair} {
		stats, err := e.Run(context.Background(), mode)
		fmt.Printf("mode=%d stats=%+v error=%v\n", mode, stats, err)
		if err != nil {
			panic(err)
		}
	}
	fmt.Printf("new=%v\nevents=%+v\n", s.New, s.Events)
}
