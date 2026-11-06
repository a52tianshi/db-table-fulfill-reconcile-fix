package mysqltable

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Schema maps your row type T to columns. Old and new tables must share it.
type Schema[T any, K comparable] struct {
	// Columns lists every column compared and copied, in the order used by
	// Values and Scan. It must include KeyColumn.
	Columns []string
	// KeyColumn is the single-column primary key: keyset paging, IN lookups
	// and UPDATE ... WHERE use it. Must be unique within every table.
	KeyColumn string
	// Key returns the identifier of a row (may carry routing fields, e.g.
	// {OrderID, UserID}); KeyValue returns KeyColumn's value for a key.
	Key      func(T) K
	KeyValue func(K) any
	// Values returns column values in Columns order (for INSERT / UPDATE).
	Values func(T) []any
	// Scan reads one row whose columns are in Columns order, e.g.
	//   func(scan func(...any) error) (Order, error) {
	//       var o Order; err := scan(&o.ID, &o.UserID, ...); return o, err }
	Scan func(scan func(dest ...any) error) (T, error)
}

// Options tunes the adapter; zero values get defaults.
type Options struct {
	// PageSize is rows per keyset scan query and max keys per IN query.
	// Default 1000.
	PageSize int
	// InsertChunk is max rows per multi-row INSERT statement. Default 500.
	InsertChunk int
	// TxOptions for repair transactions. Default READ COMMITTED (no gap locks).
	TxOptions *sql.TxOptions
	// MaxRetries re-runs a transaction after a retryable error. Default 10.
	MaxRetries int
	// IsRetryable / IsDuplicate classify driver errors. Defaults match the
	// go-sql-driver/mysql message format: "Error 1213", "Error 1205" and
	// "Error 1062". Pass typed checks if you use another driver.
	IsRetryable func(error) bool
	IsDuplicate func(error) bool
}

func mysqlErrno(err error, codes ...string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, c := range codes {
		if strings.Contains(msg, "Error "+c) {
			return true
		}
	}
	return false
}

func (o *Options) normalize() {
	if o.PageSize <= 0 {
		o.PageSize = 1000
	}
	if o.InsertChunk <= 0 {
		o.InsertChunk = 500
	}
	if o.TxOptions == nil {
		o.TxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = 10
	}
	if o.IsRetryable == nil {
		o.IsRetryable = func(err error) bool { return mysqlErrno(err, "1213", "1205") }
	}
	if o.IsDuplicate == nil {
		o.IsDuplicate = func(err error) bool { return mysqlErrno(err, "1062") }
	}
}

func (s *Schema[T, K]) validate() error {
	var errs []error
	req := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}
	req(len(s.Columns) > 0, "Columns is required")
	req(s.Key != nil && s.KeyValue != nil && s.Values != nil && s.Scan != nil, "Key, KeyValue, Values and Scan are required")
	hasKey := false
	for _, c := range s.Columns {
		hasKey = hasKey || c == s.KeyColumn
		if err := checkIdent(c); err != nil {
			errs = append(errs, err)
		}
	}
	req(hasKey, fmt.Sprintf("KeyColumn %q must be one of Columns", s.KeyColumn))
	return errors.Join(errs...)
}

func checkIdent(name string) error {
	if name == "" || strings.ContainsAny(name, "`\x00") {
		return fmt.Errorf("invalid identifier %q", name)
	}
	return nil
}

func quote(name string) string { return "`" + name + "`" }
