// Package mysqltable implements reconcile.Source and reconcile.Repairer for
// an old/new table pair in ONE MySQL instance through database/sql, with any
// sharding rule (Router) on either side. It imports no driver.
//
// Scan uses keyset paging by KeyColumn up to the MAX(KeyColumn) read when
// each table's scan starts (rows inserted later are left to dual writes).
// Repair transactions lock old rows first, then new rows, both in key order —
// your online dual-write code must take locks in the same order.
package mysqltable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

// Pair is an old/new table pair sharing one Schema and one *sql.DB.
type Pair[T any, K comparable] struct {
	db       *sql.DB
	schema   Schema[T, K]
	old, new Router[K]
	opt      Options
	q        queries
}

func NewPair[T any, K comparable](db *sql.DB, schema Schema[T, K], old, new Router[K], opt Options) (*Pair[T, K], error) {
	if db == nil || old == nil || new == nil {
		return nil, errors.New("mysqltable: db, old and new routers are required")
	}
	if err := schema.validate(); err != nil {
		return nil, fmt.Errorf("mysqltable: invalid schema: %w", err)
	}
	for _, r := range []Router[K]{old, new} {
		if len(r.Tables()) == 0 {
			return nil, errors.New("mysqltable: router has no tables")
		}
		for _, t := range r.Tables() {
			if err := checkIdent(t); err != nil {
				return nil, fmt.Errorf("mysqltable: %w", err)
			}
		}
	}
	opt.normalize()
	return &Pair[T, K]{db: db, schema: schema, old: old, new: new, opt: opt, q: newQueries(schema.Columns, schema.KeyColumn)}, nil
}

// Old and New return each side as a reconcile.Source.
func (p *Pair[T, K]) Old() reconcile.Source[T, K] { return source[T, K]{p, p.old} }
func (p *Pair[T, K]) New() reconcile.Source[T, K] { return source[T, K]{p, p.new} }

// Repairer returns the transactional side; it groups transactions by the
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
		v, err := p.schema.Scan(rows.Scan)
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

// fetch runs IN queries (chunked by PageSize) for keys on one side.
func (p *Pair[T, K]) fetch(ctx context.Context, q querier, r Router[K], keys []K, lock bool) (map[K]T, error) {
	out := make(map[K]T, len(keys))
	tables, groups := byTable(r, keys)
	for _, t := range tables {
		ks := groups[t]
		for len(ks) > 0 {
			n := min(len(ks), p.opt.PageSize)
			args := make([]any, n)
			for i, k := range ks[:n] {
				args[i] = p.schema.KeyValue(k)
			}
			rows, err := p.query(ctx, q, p.q.in(t, n, lock), args...)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t, err)
			}
			for _, v := range rows {
				out[p.schema.Key(v)] = v
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
	return s.p.fetch(ctx, s.p.db, s.r, keys, false)
}

func (s source[T, K]) Scan(ctx context.Context, yield func(T) error) error {
	for _, t := range s.r.Tables() {
		if err := s.scanTable(ctx, t, yield); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}
	return nil
}

// scanTable reads one full page, closes the cursor, then yields, so yield
// (which runs lookups and transactions) never holds a scan connection.
func (s source[T, K]) scanTable(ctx context.Context, table string, yield func(T) error) error {
	p := s.p
	top, err := p.query(ctx, p.db, p.q.last(table))
	if err != nil {
		return err
	}
	if len(top) == 0 {
		return nil // empty table
	}
	// Typed high-water mark (never MAX() as []byte: MySQL would compare a
	// string with BIGINT as DOUBLE and lose precision on snowflake ids).
	hw := p.schema.KeyValue(p.schema.Key(top[0]))
	var last any
	for {
		var rows []T
		if last == nil {
			rows, err = p.query(ctx, p.db, p.q.page(table, true), hw, p.opt.PageSize)
		} else {
			rows, err = p.query(ctx, p.db, p.q.page(table, false), last, hw, p.opt.PageSize)
		}
		if err != nil {
			return err
		}
		for _, v := range rows {
			if err := yield(v); err != nil {
				return err
			}
		}
		if len(rows) < p.opt.PageSize {
			return nil
		}
		last = p.schema.KeyValue(p.schema.Key(rows[len(rows)-1]))
	}
}

type repairer[T any, K comparable] struct{ p *Pair[T, K] }

func (r repairer[T, K]) Group(k K) string { return r.p.new.Route(k) }

func (r repairer[T, K]) InTx(ctx context.Context, fn func(reconcile.Tx[T, K]) error) error {
	for attempt := 0; ; attempt++ {
		err := r.once(ctx, fn)
		if err == nil || !r.p.opt.IsRetryable(err) || attempt >= r.p.opt.MaxRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
}

func (r repairer[T, K]) once(ctx context.Context, fn func(reconcile.Tx[T, K]) error) error {
	sqlTx, err := r.p.db.BeginTx(ctx, r.p.opt.TxOptions)
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
	old, err := t.p.fetch(ctx, t.tx, t.p.old, keys, true)
	if err != nil {
		return nil, nil, err
	}
	cur, err := t.p.fetch(ctx, t.tx, t.p.new, keys, true)
	if err != nil {
		return nil, nil, err
	}
	return old, cur, nil
}

func (t tx[T, K]) Insert(ctx context.Context, rows []T) error {
	keys := make([]K, len(rows))
	byKey := make(map[K]T, len(rows))
	for i, v := range rows {
		keys[i] = t.p.schema.Key(v)
		byKey[keys[i]] = v
	}
	tables, groups := byTable(t.p.new, keys)
	for _, table := range tables {
		ks := groups[table]
		for len(ks) > 0 {
			n := min(len(ks), t.p.opt.InsertChunk)
			args := make([]any, 0, n*t.p.q.nCols)
			for _, k := range ks[:n] {
				args = append(args, t.p.schema.Values(byKey[k])...)
			}
			if _, err := t.tx.ExecContext(ctx, t.p.q.insert(table, n), args...); err != nil {
				if t.p.opt.IsDuplicate(err) {
					return fmt.Errorf("%s: %w: %v", table, reconcile.ErrConflict, err)
				}
				return fmt.Errorf("%s: %w", table, err)
			}
			ks = ks[n:]
		}
	}
	return nil
}

func (t tx[T, K]) Update(ctx context.Context, row T) error {
	k := t.p.schema.Key(row)
	vals := t.p.schema.Values(row)
	args := make([]any, 0, len(vals))
	for i, c := range t.p.schema.Columns {
		if c != t.p.schema.KeyColumn {
			args = append(args, vals[i])
		}
	}
	args = append(args, t.p.schema.KeyValue(k))
	table := t.p.new.Route(k)
	if _, err := t.tx.ExecContext(ctx, t.p.q.update(table), args...); err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}
