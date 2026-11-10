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
	// KeyLess orders keys by KeyColumn using the same order as MySQL and
	// online writers. Required: it is used BEFORE locking IN queries are
	// chunked. For an int64 primary key, return a.ID < b.ID. For strings,
	// account for the column collation; do not sort numeric keys as strings.
	KeyLess func(a, b K) bool
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
	// (locking reads see the latest commit; gap locks may still be used
	// for duplicate-key and foreign-key checks).
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
	check(len(s.Columns) > 1, "Schema.Columns must list at least one non-key column: with only the key column, UPDATE renders \"SET WHERE\"")
	check(s.Key != nil, "Schema.Key is required")
	check(s.KeyValue != nil, "Schema.KeyValue is required")
	check(s.KeyLess != nil, "Schema.KeyLess is required")
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
	for _, side := range []struct {
		name string
		r    Router[K]
	}{{"Old", c.Old}, {"New", c.New}} {
		if side.r == nil {
			continue
		}
		if err := validateRouter(side.name, side.r); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("mysqltable: invalid config:\n%w", err)
	}
	return nil
}

// validateRouter rejects configuration that would fail at run time instead of
// here: a nil Route closure (RouterFunc(tables, nil) would panic on the first
// key), an empty or duplicated table list (a duplicated table is scanned twice
// and can be written twice per batch).
func validateRouter[K comparable](side string, r Router[K]) error {
	var errs []error
	tables := r.Tables()
	if len(tables) == 0 {
		errs = append(errs, fmt.Errorf("%s router lists no tables", side))
	}
	seen := make(map[string]struct{}, len(tables))
	for _, t := range tables {
		if err := checkIdent(t); err != nil {
			errs = append(errs, fmt.Errorf("%s router: %w", side, err))
			continue
		}
		if _, dup := seen[t]; dup {
			errs = append(errs, fmt.Errorf("%s router: duplicate table %q", side, t))
			continue
		}
		seen[t] = struct{}{}
	}
	if f, ok := r.(funcRouter[K]); ok && f.route == nil {
		errs = append(errs, fmt.Errorf("%s router: RouterFunc was built with a nil route function", side))
	}
	if err := errors.Join(errs...); err != nil {
		return err
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

// firstMySQLErrno extracts the numeric code from the first go-sql-driver/mysql
// "Error <digits><delimiter>" header in msg. Matching only the first header
// prevents user data in a Duplicate entry '...' payload from spoofing another
// MySQL error code (e.g. 'Error 1213: ...' inside Error 1062).
func firstMySQLErrno(msg string) string {
	const prefix = "Error "
	for i := 0; i < len(msg); {
		j := strings.Index(msg[i:], prefix)
		if j < 0 {
			return ""
		}
		start := i + j + len(prefix)
		end := start
		for end < len(msg) && msg[end] >= '0' && msg[end] <= '9' {
			end++
		}
		if end > start {
			if end == len(msg) {
				return msg[start:end]
			}
			switch msg[end] {
			case ' ', ':', '(', ',', ']':
				return msg[start:end]
			}
		}
		i = start
	}
	return ""
}

// mysqlErrno matches the go-sql-driver/mysql message format "Error 1213 ...".
// The code must end at a delimiter: "Error 1062" must not match "Error 10620".
func mysqlErrno(err error, codes ...string) bool {
	if err == nil {
		return false
	}
	got := firstMySQLErrno(err.Error())
	if got == "" {
		return false
	}
	for _, c := range codes {
		if got == c {
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
// ErrMisrouted (including Route(key) outside Tables()) and ErrInvalidValues
// are configuration/data errors and are never transient.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, ErrMisrouted) || errors.Is(err, ErrInvalidValues) {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}
	msg := err.Error()
	if code := firstMySQLErrno(msg); code != "" {
		switch code {
		case "1213", "1205", "2006", "2013", "1040":
			return true
		default:
			return false
		}
	}
	for _, s := range []string{"invalid connection", "broken pipe", "connection reset", "connection refused", "i/o timeout", "unexpected EOF"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
