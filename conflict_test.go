package reconcile_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

var errDeadlock = errors.New("Error 1213 (40001): Deadlock found when trying to get lock")

// flakySource fails every scan page, so the engine's retry and backoff paths run.
type flakySource struct{ r.Source[row, int] }

func (s flakySource) ScanPage(context.Context, r.Cursor, int) ([]row, r.Cursor, error) {
	return nil, nil, errDeadlock
}
func (s flakySource) Lookup(context.Context, []int) (map[int]row, error) {
	return nil, errDeadlock
}

// Cancelling the context during a backoff wait must stop Run immediately, not
// after the remaining attempts (each of which would wait the full backoff).
func TestCancelDuringRetryBackoffStopsImmediately(t *testing.T) {
	s := missingOnly()
	cfg := config(s)
	cfg.Old = flakySource{s.Old()}
	cfg.Policy, cfg.Insert = fix, r.InsertBatchLocked
	cfg.Retry = r.Retry{MaxAttempts: 20, Backoff: 2 * time.Second, MaxBackoff: 2 * time.Second,
		Retryable: func(error) bool { return true }}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := r.Run(ctx, cfg)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("cancellation waited out the backoff: Run took %v", elapsed)
	}
}

// conflictInsert always reports a write conflict on Insert, like a hot shard.
type conflictInsert struct {
	r.Tx[row, int]
	inserts *int
	failFor int
}

func (c conflictInsert) Insert(ctx context.Context, rows []row) error {
	*c.inserts++
	if *c.inserts <= c.failFor {
		return fmt.Errorf("orders_00: %w", r.ErrConflict)
	}
	return c.Tx.Insert(ctx, rows)
}

type conflictRepairer struct {
	*memtable.Store[row, int]
	inserts int
	failFor int
}

func (c *conflictRepairer) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	return c.Store.InTx(ctx, func(tx r.Tx[row, int]) error {
		return fn(conflictInsert{Tx: tx, inserts: &c.inserts, failFor: c.failFor})
	})
}

// A persistently conflicting insert must stop after maxConflictRounds rechecks
// with an ErrConflict, instead of looping forever or reporting success.
func TestPersistentConflictIsBoundedAndReported(t *testing.T) {
	s := missingOnly()
	cfg := config(s)
	cfg.Policy, cfg.Insert = fix, r.InsertBatchSnapshot
	rep := &conflictRepairer{Store: s, failFor: 1000}
	cfg.Repairer = rep

	done := make(chan struct{})
	var err error
	go func() {
		_, err = r.Run(context.Background(), cfg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not terminate: the conflict recheck loop is unbounded")
	}
	if !errors.Is(err, r.ErrConflict) {
		t.Fatalf("want an error wrapping ErrConflict, got %v", err)
	}
	// 1 snapshot insert + one insert attempt per recheck round (max 4: rounds 0..3).
	if rep.inserts > 5 {
		t.Fatalf("too many insert attempts against a conflicting shard: %d", rep.inserts)
	}
}

// Once the shard stops conflicting, the recheck must recover without a second
// full scan: the rows are already visible under lock.
func TestConflictRecoversWithinRounds(t *testing.T) {
	s := missingOnly()
	cfg := config(s)
	cfg.Policy, cfg.Insert = fix, r.InsertBatchSnapshot
	rep := &conflictRepairer{Store: s, failFor: 2}
	cfg.Repairer = rep

	got, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run failed after transient conflicts: %v", err)
	}
	// A snapshot-insert run keeps the scan-time findings, so the second run is
	// the one that must find nothing left to repair.
	second, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if !second.Consistent() {
		t.Fatalf("second run still finds differences: first=%+v second=%+v", got, second)
	}
	if rep.inserts < 3 {
		t.Fatalf("expected the recheck rounds to be exercised, only %d insert calls", rep.inserts)
	}
}
