// Command e2e drives the MySQL end-to-end test. Subcommands:
//
//	init      step 1: recreate schema, randomly fill the old table
//	writer    step 2: simulated online traffic, transactional dual write
//	reconcile step 3/4: run the framework once (-expect-clean for step 4)
//	verify    step 6: independent row-by-row old/new comparison
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-sql-driver/mysql"
)

const defaultDSN = "root@tcp(127.0.0.1:3306)/reconcile_e2e"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: e2e <init|writer|reconcile|verify> [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dsn := fs.String("dsn", envOr("E2E_MYSQL_DSN", defaultDSN), "MySQL DSN (env E2E_MYSQL_DSN)")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var run func(context.Context, *sql.DB) error
	switch cmd {
	case "init":
		run = initCmd(fs)
	case "writer":
		run = writerCmd(fs)
	case "reconcile":
		run = reconcileCmd(fs)
	case "verify":
		run = verifyCmd(fs)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", cmd)
		os.Exit(2)
	}
	_ = fs.Parse(args)
	db, err := open(ctx, *dsn, cmd == "init")
	if err == nil {
		err = run(ctx, db)
		db.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] FAIL: %v\n", cmd, err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// open connects; for init it first creates the database named in the DSN.
func open(ctx context.Context, dsn string, create bool) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.DBName == "" {
		return nil, fmt.Errorf("DSN must name a database")
	}
	if create {
		admin := cfg.Clone()
		admin.DBName = ""
		db, err := sql.Open("mysql", admin.FormatDSN())
		if err != nil {
			return nil, err
		}
		_, err = db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+cfg.DBName+"`")
		db.Close()
		if err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	return db, db.PingContext(ctx)
}
