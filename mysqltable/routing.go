package mysqltable

import (
	"errors"
	"fmt"
)

// ErrMisrouted identifies a row found in a physical table other than Route(key).
// It is a data/configuration error, not transient. Rows are never moved/deleted.
//
// ErrMisrouted is also the (only) sentinel for a Router whose Route(key)
// returns a table that is not listed in Tables(): Lookup, Lock, Insert and
// Update refuse such keys with a *MisroutedError (Table = the unlisted table,
// Expected = ""), before any SQL is sent. There is no separate
// "invalid route" sentinel; use errors.Is(err, ErrMisrouted) and
// errors.As(err, **MisroutedError) for details.
var ErrMisrouted = errors.New("mysqltable: misrouted row")

// ErrInvalidValues reports that Schema.Values returned a slice whose length is
// not len(Schema.Columns) for some row. Tx.Insert validates every row (not
// just the total argument count) and Tx.Update validates its row, before any
// SQL is sent; the transaction is then rolled back by the caller.
//
// It is a configuration error: IsTransient reports false, so retrying cannot
// help. Use errors.Is(err, ErrInvalidValues) to tell it apart from data
// conflicts and transient MySQL errors.
var ErrInvalidValues = errors.New("mysqltable: invalid column values")

// MisroutedError describes the row and both tables. It wraps ErrMisrouted so
// callers can use errors.Is and errors.As after Run adds context.
type MisroutedError struct {
	Key      any
	Table    string
	Expected string
}

func (e *MisroutedError) Error() string {
	return fmt.Sprintf("%v: key=%v physical=%q expected=%q", ErrMisrouted, e.Key, e.Table, e.Expected)
}
func (e *MisroutedError) Unwrap() error { return ErrMisrouted }

func checkRoute[K comparable](r Router[K], k K) (string, error) {
	t := r.Route(k)
	for _, declared := range r.Tables() {
		if declared == t {
			return t, nil
		}
	}
	return "", &MisroutedError{Key: k, Table: t, Expected: ""}
}

func checkRoutes[K comparable](r Router[K], keys []K) error {
	if len(keys) == 0 {
		return nil
	}
	declared := r.Tables()
	valid := make(map[string]struct{}, len(declared))
	for _, t := range declared {
		valid[t] = struct{}{}
	}
	for _, k := range keys {
		t := r.Route(k)
		if _, ok := valid[t]; !ok {
			return &MisroutedError{Key: k, Table: t, Expected: ""}
		}
	}
	return nil
}

func (p *Pair[T, K]) checkRouting(router Router[K], table string, rows []T) error {
	for _, row := range rows {
		k := p.cfg.Schema.Key(row)
		expected := router.Route(k)
		if expected != table {
			return &MisroutedError{Key: k, Table: table, Expected: expected}
		}
	}
	return nil
}

func (p *Pair[T, K]) values(row T) ([]any, error) {
	vals := p.cfg.Schema.Values(row)
	if len(vals) != len(p.cfg.Schema.Columns) {
		return nil, fmt.Errorf("%w: Schema.Values returned %d values for %d columns (key=%v)",
			ErrInvalidValues, len(vals), len(p.cfg.Schema.Columns), p.cfg.Schema.Key(row))
	}
	return vals, nil
}
