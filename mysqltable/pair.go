// Package mysqltable implements reconcile.Source and reconcile.Repairer for
// an old/new table pair in ONE MySQL instance through database/sql, with any
// sharding rule (Router) on either side. It imports no driver.
//
// Every method makes exactly one attempt; retries belong to the engine
// (reconcile.Config.Retry — IsTransient is a ready-made classifier).
//
// ScanPage uses keyset paging by KeyColumn, per table, up to the key of the
// highest row seen when that table's scan started. This is a key bound,
// not a temporal snapshot: later inserts below it may still be observed. Repair transactions lock old rows first, then new
// rows, each in key order — online dual-write code must use the same order.
package mysqltable

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

// Pair is an old/new table pair sharing one Schema and one *sql.DB.
type Pair[T any, K comparable] struct {
	cfg PairConfig[T, K]
	q   queries
}

// NewPair validates cfg (all fields required) and builds the pair.
func NewPair[T any, K comparable](cfg PairConfig[T, K]) (*Pair[T, K], error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Pair[T, K]{cfg: cfg, q: newQueries(cfg.Schema.Columns, cfg.Schema.KeyColumn)}, nil
}

// Old and New return each side as a reconcile.Source.
func (p *Pair[T, K]) Old() reconcile.Source[T, K] { return source[T, K]{p, p.cfg.Old} }
func (p *Pair[T, K]) New() reconcile.Source[T, K] { return source[T, K]{p, p.cfg.New} }

// Repairer returns the transactional side. It groups transactions by the
// new-side table, so one transaction never spans two new shards.
func (p *Pair[T, K]) Repairer() reconcile.Repairer[T, K] { return repairer[T, K]{p} }

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (p *Pair[T, K]) query(ctx context.Context, q querier, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := p.cfg.Schema.Scan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// byTable groups keys by table in sorted table order (stable lock order).
func byTable[K comparable](r Router[K], keys []K) ([]string, map[string][]K) {
	groups := map[string][]K{}
	for _, k := range keys {
		t := r.Route(k)
		groups[t] = append(groups[t], k)
	}
	tables := make([]string, 0, len(groups))
	for t := range groups {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	return tables, groups
}

// fetch runs IN queries (chunked by InChunk) for keys on one side.
func (p *Pair[T, K]) fetch(ctx context.Context, q querier, r Router[K], keys []K, lock bool) (map[K]T, error) {
	out := make(map[K]T, len(keys))
	tables, groups := byTable(r, keys)
	for _, t := range tables {
		ks := groups[t]
		if lock {
			sort.SliceStable(ks, func(i, j int) bool { return p.cfg.Schema.KeyLess(ks[i], ks[j]) })
		}
		for len(ks) > 0 {
			n := min(len(ks), p.cfg.InChunk)
			args := make([]any, n)
			for i, k := range ks[:n] {
				args[i] = p.cfg.Schema.KeyValue(k)
			}
			rows, err := p.query(ctx, q, p.q.in(t, n, lock), args...)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t, err)
			}
			if err := p.checkRouting(r, t, rows); err != nil {
				return nil, err
			}
			for _, v := range rows {
				out[p.cfg.Schema.Key(v)] = v
			}
			ks = ks[n:]
		}
	}
	return out, nil
}

type source[T any, K comparable] struct {
	p *Pair[T, K]
	r Router[K]
}

func (s source[T, K]) Lookup(ctx context.Context, keys []K) (map[K]T, error) {
	return s.p.fetch(ctx, s.p.cfg.DB, s.r, keys, false)
}

// scanCursor: table index, that table's high-water key (nil = not started)
// and the last key returned (nil = first page).
type scanCursor struct {
	table    int
	hw, last any
}

func (s source[T, K]) ScanPage(ctx context.Context, c reconcile.Cursor, limit int) ([]T, reconcile.Cursor, error) {
	var cur scanCursor
	if c != nil {
		var ok bool
		if cur, ok = c.(scanCursor); !ok {
			return nil, nil, fmt.Errorf("mysqltable: foreign cursor %T", c)
		}
	}
	p, schema, tables := s.p, s.p.cfg.Schema, s.r.Tables()
	for cur.table < len(tables) {
		t := tables[cur.table]
		if cur.hw == nil {
			// Typed high-water mark from the last row (never MAX() as []byte:
			// MySQL compares a string with BIGINT as DOUBLE and loses
			// precision on snowflake ids).
			top, err := p.query(ctx, p.cfg.DB, p.q.last(t))
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", t, err)
			}
			if err := p.checkRouting(s.r, t, top); err != nil {
				return nil, nil, err
			}
			if len(top) == 0 {
				cur = scanCursor{table: cur.table + 1} // empty table
				continue
			}
			cur.hw = schema.KeyValue(schema.Key(top[0]))
		}
		var rows []T
		var err error
		if cur.last == nil {
			rows, err = p.query(ctx, p.cfg.DB, p.q.page(t, true), cur.hw, limit)
		} else {
			rows, err = p.query(ctx, p.cfg.DB, p.q.page(t, false), cur.last, cur.hw, limit)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", t, err)
		}
		if err := p.checkRouting(s.r, t, rows); err != nil {
			return nil, nil, err
		}
		var next reconcile.Cursor
		switch {
		case len(rows) == limit:
			next = scanCursor{cur.table, cur.hw, schema.KeyValue(schema.Key(rows[len(rows)-1]))}
		case cur.table+1 < len(tables):
			next = scanCursor{table: cur.table + 1}
		}
		return rows, next, nil
	}
	return nil, nil, nil
}

type repairer[T any, K comparable] struct{ p *Pair[T, K] }

func (r repairer[T, K]) Group(k K) string { return r.p.cfg.New.Route(k) }

func (r repairer[T, K]) InTx(ctx context.Context, fn func(reconcile.Tx[T, K]) error) error {
	sqlTx, err := r.p.cfg.DB.BeginTx(ctx, r.p.cfg.TxOptions)
	if err != nil {
		return err
	}
	defer sqlTx.Rollback()
	if err := fn(tx[T, K]{r.p, sqlTx}); err != nil {
		return err
	}
	return sqlTx.Commit()
}

type tx[T any, K comparable] struct {
	p  *Pair[T, K]
	tx *sql.Tx
}

func (t tx[T, K]) Lock(ctx context.Context, keys []K) (map[K]T, map[K]T, error) {
	old, err := t.p.fetch(ctx, t.tx, t.p.cfg.Old, keys, true)
	if err != nil {
		return nil, nil, err
	}
	cur, err := t.p.fetch(ctx, t.tx, t.p.cfg.New, keys, true)
	if err != nil {
		return nil, nil, err
	}
	return old, cur, nil
}

func (t tx[T, K]) Insert(ctx context.Context, rows []T) error {
	schema := t.p.cfg.Schema
	keys := make([]K, len(rows))
	byKey := make(map[K]T, len(rows))
	for i, v := range rows {
		keys[i] = schema.Key(v)
		byKey[keys[i]] = v
	}
	tables, groups := byTable(t.p.cfg.New, keys)
	for _, table := range tables {
		ks := groups[table]
		for len(ks) > 0 {
			n := min(len(ks), t.p.cfg.InsertChunk)
			args := make([]any, 0, n*t.p.q.nCols)
			for _, k := range ks[:n] {
				args = append(args, schema.Values(byKey[k])...)
			}
			if _, err := t.tx.ExecContext(ctx, t.p.q.insert(table, n), args...); err != nil {
				if t.p.cfg.IsDuplicate(err) {
					return fmt.Errorf("%s: %w: %w", table, reconcile.ErrConflict, err)
				}
				return fmt.Errorf("%s: %w", table, err)
			}
			ks = ks[n:]
		}
	}
	return nil
}

func (t tx[T, K]) Update(ctx context.Context, row T) error {
	schema := t.p.cfg.Schema
	k := schema.Key(row)
	vals := schema.Values(row)
	args := make([]any, 0, len(vals))
	for i, c := range schema.Columns {
		if c != schema.KeyColumn {
			args = append(args, vals[i])
		}
	}
	args = append(args, schema.KeyValue(k))
	table := t.p.cfg.New.Route(k)
	if _, err := t.tx.ExecContext(ctx, t.p.q.update(table), args...); err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}
