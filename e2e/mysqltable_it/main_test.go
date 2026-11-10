package mysqltable_it

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// The integration tests always rebuild tables in the shared reconcile_it
// database, so two suites running at once corrupt each other. Take a MySQL
// advisory lock for the whole package run; it is held on a dedicated
// connection and released automatically if the process dies.
const suiteLock = "db_table_fulfill_reconcile_it_suite"

func TestMain(m *testing.M) {
	code, err := withSuiteLock(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mysqltable_it: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func withSuiteLock(m *testing.M) (int, error) {
	dsn := os.Getenv("E2E_MYSQL_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:3306)/reconcile_e2e"
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return 0, fmt.Errorf("parse DSN: %w", err)
	}
	cfg.DBName = ""
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		return m.Run(), nil // unreachable MySQL: let the tests skip themselves
	}
	defer conn.Close()
	var got int
	// MySQL is reachable from here on. Failing to take the suite lock must
	// fail the run: continuing unlocked would let two suites rebuild the
	// shared reconcile_it tables under each other and produce false results.
	if err := conn.QueryRowContext(context.Background(), "SELECT GET_LOCK(?, 900)", suiteLock).Scan(&got); err != nil {
		return 0, fmt.Errorf("GET_LOCK(%q): %w", suiteLock, err)
	}
	if got != 1 {
		return 0, fmt.Errorf("GET_LOCK(%q) returned %d: suite lock not acquired within 900s", suiteLock, got)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", suiteLock)
	}()
	return m.Run(), nil
}
