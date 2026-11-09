package reconcile_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

// The data commits through the real in-memory adapter, but the caller sees
// a lost response. This models the uncertainty at the Repairer boundary;
// it does not simulate MySQL transport or durability.
type lostCommitResponse struct {
	*memtable.Store[row, int]
	err  error
	lost bool
}

func (s *lostCommitResponse) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	if err := s.Store.InTx(ctx, fn); err != nil {
		return err
	}
	if !s.lost {
		s.lost = true
		return s.err
	}
	return nil
}

// Catches replaying a committed write without rereading, duplicate inserts,
// lost rows, and treating repair counters as proof of data equivalence.
func TestLostCommitResponsePreservesData(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		for _, different := range []bool{false, true} {
			name := "missing/"
			if different {
				name = "different/"
			}
			t.Run(name+mode.String(), func(t *testing.T) {
				old := []row{{1, "fresh-a", ""}, {2, "fresh-b", ""}}
				current := []row{{9, "keep-extra", ""}}
				if different {
					current = append(current, row{1, "stale-a", ""}, row{2, "stale-b", ""})
				}
				s := memtable.New(key, old, current)
				sentinel := errors.New("commit response lost")
				cfg := config(s)
				cfg.Policy, cfg.Insert = fix, mode
				cfg.Repairer = &lostCommitResponse{Store: s, err: sentinel}
				cfg.Retry.MaxAttempts = 2
				cfg.Retry.Retryable = func(err error) bool { return errors.Is(err, sentinel) }
				rep := mustRun(t, cfg)
				want := map[int]row{1: {1, "fresh-a", ""}, 2: {2, "fresh-b", ""}, 9: {9, "keep-extra", ""}}
				if !reflect.DeepEqual(newRows(s), want) || rep.Retries != 1 || rep.Stale == 0 || rep.Extra != 1 {
					t.Fatalf("rows=%v report=%v", newRows(s), rep)
				}
				// The same literal result must survive a new Run. Extra stays
				// visible; it is not deleted to make Consistent return true.
				again := mustRun(t, cfg)
				if !reflect.DeepEqual(newRows(s), want) || again.Inserted+again.Updated != 0 || again.Missing+again.Different != 0 || again.Extra != 1 {
					t.Fatalf("rerun rows=%v report=%v", newRows(s), again)
				}
			})
		}
	}
}

// Catches rolling back already committed data on Run failure, or relying on
// process-local candidate state to resume after an exhausted retry policy.
func TestLostCommitResponseCanRecoverOnNextRun(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		t.Run(mode.String(), func(t *testing.T) {
			s := memtable.New(key, []row{{1, "fresh", ""}}, nil)
			sentinel := errors.New("commit response lost")
			cfg := config(s)
			cfg.Policy, cfg.Insert = fix, mode
			cfg.Repairer = &lostCommitResponse{Store: s, err: sentinel}
			rep, err := r.Run(context.Background(), cfg)
			if !errors.Is(err, sentinel) || rep.Inserted != 0 || newRows(s)[1] != (row{1, "fresh", ""}) {
				t.Fatalf("rows=%v report=%v err=%v", newRows(s), rep, err)
			}
			cfg.Repairer = s
			again := mustRun(t, cfg)
			if !again.Consistent() || again.Inserted+again.Updated != 0 || len(newRows(s)) != 1 || newRows(s)[1] != (row{1, "fresh", ""}) {
				t.Fatalf("recovery rows=%v report=%v", newRows(s), again)
			}
		})
	}
}
