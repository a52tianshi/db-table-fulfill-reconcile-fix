package reconcile_test

import (
	"context"
	"errors"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

var errAckLost = errors.New("connection lost while waiting for COMMIT ack")

// ackRetryAll retries everything, so a lost-ack error is retried by the engine
// exactly like a transient driver error would be in production.
var ackRetryAll = r.Retry{MaxAttempts: 5, Backoff: time.Millisecond, MaxBackoff: time.Millisecond,
	Retryable: func(error) bool { return true }}

// lostAck commits the first `lose` transactions for real, then reports them as
// failed — the "commit outcome unknown" case. It also records how many rows the
// faulted transaction wrote with Insert, so a test can prove the fault really
// landed on an insert transaction instead of an update one.
type lostAck struct {
	*memtable.Store[row, int]
	lose, calls    int
	faultedInserts int
}

type insertSpy struct {
	inner   r.Tx[row, int]
	inserts int
}

func (s *insertSpy) Lock(ctx context.Context, keys []int) (map[int]row, map[int]row, error) {
	return s.inner.Lock(ctx, keys)
}

func (s *insertSpy) Insert(ctx context.Context, rows []row) error {
	s.inserts += len(rows)
	return s.inner.Insert(ctx, rows)
}

func (s *insertSpy) Update(ctx context.Context, v row) error { return s.inner.Update(ctx, v) }

func (l *lostAck) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	l.calls++
	var spy *insertSpy
	err := l.Store.InTx(ctx, func(tx r.Tx[row, int]) error {
		spy = &insertSpy{inner: tx}
		return fn(spy)
	})
	if err != nil {
		return err
	}
	if l.calls <= l.lose {
		l.faultedInserts = spy.inserts
		return errAckLost
	}
	return nil
}

// missingOnly builds a store where every old row is missing from the new side,
// so the first transaction of a Fix run is an Insert transaction.
func missingOnly() *memtable.Store[row, int] {
	return memtable.New(key,
		[]row{{1, "a", ""}, {2, "b", ""}, {3, "c", ""}, {4, "d", ""}},
		[]row{})
}

// A rerun after a lost commit ack must reread, see the rows already fixed, and
// neither write twice nor fail. The data plane ends identical to a run without
// the fault; only the counters move from Inserted to Stale.
//
// The fault here is aimed at an insert transaction (all findings are Missing),
// which is the path a batch-split fixture with one Missing row per batch can
// silently miss.
func TestLostCommitAckOnInsertIsIdempotent(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		t.Run(mode.String(), func(t *testing.T) {
			clean := missingOnly()
			cfg := config(clean)
			cfg.Policy, cfg.Insert = fix, mode
			want := mustRun(t, cfg)
			if want.Inserted == 0 {
				t.Fatalf("fixture is wrong: clean run inserted nothing: %+v", want)
			}

			s := missingOnly()
			rep := &lostAck{Store: s, lose: 1}
			cfg = config(s)
			cfg.Policy, cfg.Insert, cfg.Retry, cfg.Repairer = fix, mode, ackRetryAll, rep
			got := mustRun(t, cfg)

			if rep.faultedInserts == 0 {
				t.Fatalf("fault did not land on an insert transaction (mode=%s); the test would pass without covering the insert retry path", mode)
			}

			a, b := newRows(clean), newRows(s)
			if len(a) != len(b) {
				t.Fatalf("data differs: clean=%v lostAck=%v", a, b)
			}
			for k, v := range a {
				if b[k] != v {
					t.Fatalf("key %d: clean=%v lostAck=%v", k, v, b[k])
				}
			}
			if got.Retries < 1 {
				t.Fatalf("lost ack was not retried: %+v", got)
			}
			// The committed-but-unacknowledged rows are rediscovered as already
			// consistent: they must not be inserted twice, and they must still be
			// accounted for (as stale) rather than silently dropped.
			faulted := int64(rep.faultedInserts)
			if got.Inserted != want.Inserted-faulted {
				t.Fatalf("rows from the lost-ack transaction were written again: want inserted=%d (clean %d - faulted %d), got %+v",
					want.Inserted-faulted, want.Inserted, faulted, got)
			}
			if got.Inserted+got.Stale != want.Inserted {
				t.Fatalf("lost-ack rows are not accounted for: clean=%+v lostAck=%+v (faulted inserts=%d)", want, got, faulted)
			}
		})
	}
}

// The same guarantee for an update transaction: the first transaction carries a
// Different finding, so the lost ack lands on Update and the rerun must reread.
func TestLostCommitAckOnUpdateIsIdempotent(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		t.Run(mode.String(), func(t *testing.T) {
			clean := fixture()
			cfg := config(clean)
			cfg.Policy, cfg.Insert = fix, mode
			want := mustRun(t, cfg)

			s := fixture()
			rep := &lostAck{Store: s, lose: 1}
			cfg = config(s)
			cfg.Policy, cfg.Insert, cfg.Retry, cfg.Repairer = fix, mode, ackRetryAll, rep
			got := mustRun(t, cfg)

			if a, b := newRows(clean), newRows(s); len(a) != len(b) {
				t.Fatalf("data differs: clean=%v lostAck=%v", a, b)
			} else {
				for k, v := range a {
					if b[k] != v {
						t.Fatalf("key %d: clean=%v lostAck=%v", k, v, b[k])
					}
				}
			}
			if got.Retries != 1 {
				t.Fatalf("want exactly one retry, got %+v", got)
			}
			if got.Inserted+got.Updated+got.Stale < want.Inserted+want.Updated {
				t.Fatalf("lost writes are not accounted for: clean=%+v lostAck=%+v", want, got)
			}
			if got.Inserted+got.Updated >= want.Inserted+want.Updated {
				t.Fatalf("expected the lost commit to be counted as stale, not written: clean=%+v lostAck=%+v", want, got)
			}
		})
	}
}
