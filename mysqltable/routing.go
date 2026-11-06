package mysqltable

import (
	"errors"
	"fmt"
)

// ErrMisrouted identifies a row found in a physical table other than Route(key).
// It is a data/configuration error, not transient. Rows are never moved/deleted.
var ErrMisrouted = errors.New("mysqltable: misrouted row")

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
