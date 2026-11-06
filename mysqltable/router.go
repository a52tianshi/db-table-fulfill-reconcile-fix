package mysqltable

import "fmt"

// Router is the sharding rule of one side: which physical table holds a key.
type Router[K comparable] interface {
	// Tables lists every physical table, in scan order.
	Tables() []string
	// Route returns the table that holds (or would hold) key. It must be one
	// of Tables() and must depend only on immutable key fields.
	Route(key K) string
}

// Single is an unsharded table.
func Single[K comparable](table string) Router[K] { return single[K](table) }

type single[K comparable] string

func (s single[K]) Tables() []string { return []string{string(s)} }
func (s single[K]) Route(K) string   { return string(s) }

// Mod routes key to fmt.Sprintf(format, shardKey(key) % n), e.g.
// Mod("orders_new_%02d", 100, func(k Key) int64 { return k.UserID }).
// Negative shard keys are routed by their absolute remainder.
func Mod[K comparable](format string, n int, shardKey func(K) int64) Router[K] {
	if n <= 0 || shardKey == nil {
		panic("mysqltable.Mod: n must be > 0 and shardKey non-nil")
	}
	tables := make([]string, n)
	for i := range tables {
		tables[i] = fmt.Sprintf(format, i)
	}
	return modRouter[K]{tables: tables, shardKey: shardKey}
}

type modRouter[K comparable] struct {
	tables   []string
	shardKey func(K) int64
}

func (m modRouter[K]) Tables() []string { return m.tables }
func (m modRouter[K]) Route(k K) string {
	i := m.shardKey(k) % int64(len(m.tables))
	if i < 0 {
		i = -i
	}
	return m.tables[i]
}

// RouterFunc builds a Router from a table list and a routing function.
func RouterFunc[K comparable](tables []string, route func(K) string) Router[K] {
	return funcRouter[K]{tables, route}
}

type funcRouter[K comparable] struct {
	tables []string
	route  func(K) string
}

func (f funcRouter[K]) Tables() []string { return f.tables }
func (f funcRouter[K]) Route(k K) string { return f.route(k) }
