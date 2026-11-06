package mysqltable

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// Schema maps your row type T to columns. Old and new tables must share it.
// Every field is required.
type Schema[T any, K comparable] struct {
	// Columns lists every column compared and copied, in the order used by
	// Values and Scan. It must include KeyColumn.
	Columns []string
	// KeyColumn is the single-column primary key: keyset paging, IN lookups
	// and UPDATE ... WHERE use it. Must be unique within every table.
	KeyColumn string
	// Key returns the identifier of a row (may carry routing fields, e.g.
	// {OrderID, UserID}); KeyValue returns KeyColumn's value for a key, typed
	// (int64, string...), never a []byte.
	Key      func(T) K
	KeyValue func(K) any
	// Values returns column values in Columns order (for INSERT / UPDATE).
	Values func(T) []any
	// Scan reads one row whose columns are in Columns order, e.g.
	//   func(scan func(...any) error) (Order, error) {
	//       var o Order; err := scan(&o.ID, &o.UserID, ...); return o, err }
	Scan func(scan func(dest ...any) error) (T, error)
}

// PairConfig is everything NewPair needs. Every field is required.
type PairConfig[T any, K comparable] struct {
	DB     *sql.DB      // one MySQL instance holding the old and all new tables
	Schema Schema[T, K] // shared by old and new tables
	Old    Router[K]    // sharding rule of the old side, e.g. Single
	New    Router[K]    // sharding rule of the new side, e.g. Mod

	// TxOptions for repair transactions; READ COMMITTED is recommended
	// (locking reads see the latest commit, no gap locks).
	TxOptions *sql.TxOptions
	InChunk   int // max keys per `IN (...)` query, > 0 (e.g. 1000)
	// InsertChunk is max rows per multi-row INSERT statement, > 0 (e.g. 500).
	InsertChunk int
	// IsDuplicate recognizes a duplicate-key error, e.g. IsDuplicateKey.
	IsDuplicate func(error) bool
}

func (c *PairConfig[T, K]) validate() error {
	var errs []error
	check := func(ok bool, format string, a ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, a...))
		}
	}
	s := c.Schema
	check(c.DB != nil, "DB is required")
	check(c.Old != nil && len(c.Old.Tables()) > 0, "Old router is required and must list tables")
	check(c.New != nil && len(c.New.Tables()) > 0, "New router is required and must list tables")
	check(c.TxOptions != nil, "TxOptions is required")
	check(c.InChunk > 0, "InChunk must be > 0, got %d", c.InChunk)
	check(c.InsertChunk > 0, "InsertChunk must be > 0, got %d", c.InsertChunk)
	check(c.IsDuplicate != nil, "IsDuplicate is required")
	check(len(s.Columns) > 0, "Schema.Columns is required")
	check(s.Key != nil, "Schema.Key is required")
	check(s.KeyValue != nil, "Schema.KeyValue is required")
	check(s.Values != nil, "Schema.Values is required")
	check(s.Scan != nil, "Schema.Scan is required")
	hasKey := false
	for _, col := range s.Columns {
		hasKey = hasKey || col == s.KeyColumn
		if err := checkIdent(col); err != nil {
			errs = append(errs, err)
		}
	}
	check(hasKey, "Schema.KeyColumn %q must be one of Columns", s.KeyColumn)
	for _, r := range []Router[K]{c.Old, c.New} {
		if r == nil {
			continue
		}
		for _, t := range r.Tables() {
			if err := checkIdent(t); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("mysqltable: invalid config:\n%w", err)
	}
	return nil
}

func checkIdent(name string) error {
	if name == "" || strings.ContainsAny(name, "`\x00") {
		return fmt.Errorf("invalid identifier %q", name)
	}
	return nil
}

func quote(name string) string { return "`" + name + "`" }

// mysqlErrno matches the go-sql-driver/mysql message format "Error 1213 ...".
func mysqlErrno(err error, codes ...string) bool {
	msg := err.Error()
	for _, c := range codes {
		if strings.Contains(msg, "Error "+c) {
			return true
		}
	}
	return false
}

// IsDuplicateKey reports MySQL error 1062 (go-sql-driver message format).
func IsDuplicateKey(err error) bool { return err != nil && mysqlErrno(err, "1062") }

// IsTransient is a ready-made reconcile.Retry.Retryable for MySQL: deadlock
// (1213), lock wait timeout (1205), server gone / lost connection
// (2006, 2013), too many connections (1040), and broken connections.
// Anything else (syntax, missing table, duplicate key...) is not retried.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}
	if mysqlErrno(err, "1213", "1205", "2006", "2013", "1040") {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"invalid connection", "broken pipe", "connection reset", "connection refused", "i/o timeout", "unexpected EOF"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
