package reconcile_test

import (
	"context"
	"errors"
	"io"
	"testing"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

type row struct {
	ID    int
	Value string
	Note  string // tolerated in some tests
}

func key(v row) int { return v.ID }

// fixture: 1 equal, 2 different, 3 missing, 4 extra.
func fixture() *memtable.Store[row, int] {
	return memtable.New(key,
		[]row{{1, "same", ""}, {2, "old", ""}, {3, "missing", ""}},
		[]row{{1, "same", ""}, {2, "stale", ""}, {4, "extra", ""}})
}

// recorder collects findings.
type recorder struct{ got []r.Finding[row, int] }

func (c *recorder) Notify(_ context.Context, f r.Finding[row, int]) error {
	c.got = append(c.got, f)
	return nil
}

func config(s *memtable.Store[row, int], n r.Notifier[row, int]) r.Config[row, int] {
	if n == nil {
		n = r.LogNotifier[row, int]{W: io.Discard}
	}
	return r.Config[row, int]{
		Old: s.Old(), New: s.New(), Key: key,
		Compare: r.Exact[row](), Repairer: s, Notifier: n,
		BatchSize: 2,
	}
}

func run(t *testing.T, cfg r.Config[row, int]) r.Report {
	t.Helper()
	e, err := r.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func newRows(s *memtable.Store[row, int]) map[int]row {
	out := map[int]row{}
	for _, v := range s.Rows(false) {
		out[v.ID] = v
	}
	return out
}

func TestWarnPolicyNeverWrites(t *testing.T) {
	s := fixture()
	rec := &recorder{}
	rep := run(t, config(s, rec))
	if rep.Missing != 1 || rep.Different != 1 || rep.Extra != 1 || rep.Warned != 3 || rep.Inserted+rep.Updated != 0 {
		t.Fatalf("%+v", rep)
	}
	if s.Tx != 3 || len(rec.got) != 3 || newRows(s)[2].Value != "stale" {
		t.Fatalf("tx=%d findings=%+v", s.Tx, rec.got)
	}
}

func TestFixPolicyInsertsOverwritesKeepsExtra(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		t.Run(mode.String(), func(t *testing.T) {
			s := fixture()
			cfg := config(s, nil)
			cfg.Policy = r.Policy{Missing: r.Fix, Different: r.Fix}
			cfg.Insert = mode
			rep := run(t, cfg)
			got := newRows(s)
			if rep.Inserted != 1 || rep.Updated != 1 || rep.Extra != 1 || got[2].Value != "old" || got[3].Value != "missing" || got[4].Value != "extra" {
				t.Fatalf("%+v %+v", rep, got)
			}
			if again := run(t, cfg); !again.Consistent() && again.Extra != 1 {
				t.Fatalf("second run %+v", again)
			}
		})
	}
}

func TestPolicyPerKind(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	cfg.Policy = r.Policy{Missing: r.Fix, Different: r.Warn}
	rep := run(t, cfg)
	got := newRows(s)
	if rep.Inserted != 1 || rep.Updated != 0 || got[3].Value != "missing" || got[2].Value != "stale" {
		t.Fatalf("%+v %+v", rep, got)
	}
}

func TestToleratedFieldIsConsistent(t *testing.T) {
	s := memtable.New(key, []row{{1, "v", "a"}}, []row{{1, "v", "b"}})
	cfg := config(s, nil)
	cmp, err := r.Struct[row]([]string{"Note"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Compare = cmp
	cfg.Policy.Different = r.Fix
	if rep := run(t, cfg); rep.Suspected != 0 || rep.Updated != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestDifferentFindingCarriesFields(t *testing.T) {
	s := memtable.New(key, []row{{1, "a", "x"}}, []row{{1, "b", "x"}})
	cfg := config(s, nil)
	cmp, _ := r.Struct[row](nil)
	cfg.Compare = cmp
	rec := &recorder{}
	cfg.Notifier = rec
	run(t, cfg)
	if len(rec.got) != 1 || len(rec.got[0].Fields) != 1 || rec.got[0].Fields[0] != "Value" {
		t.Fatalf("%+v", rec.got)
	}
}

// staleRepairer changes the data right before each transaction, like online
// traffic arriving between the first comparison and the recheck.
type staleRepairer struct {
	*memtable.Store[row, int]
	before func()
}

func (s staleRepairer) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	s.before()
	return s.Store.InTx(ctx, fn)
}

func TestStaleCandidateIsSkipped(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	cfg.Policy = r.Policy{Missing: r.Fix, Different: r.Fix}
	cfg.SkipExtraCheck = true
	cfg.BatchSize = 10 // both candidates found before the first transaction
	cfg.Repairer = staleRepairer{s, func() {
		s.Set(false, row{2, "old", ""})     // dual write already fixed it
		s.Set(false, row{3, "missing", ""}) // and inserted it
	}}
	rep := run(t, cfg)
	if rep.Suspected != 2 || rep.Stale != 2 || rep.Missing+rep.Different != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestRecheckUsesFreshOldValue(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	cfg.Policy.Missing = r.Fix
	cfg.Repairer = staleRepairer{s, func() { s.Set(true, row{3, "updated", ""}) }}
	run(t, cfg)
	if got := newRows(s)[3].Value; got != "updated" {
		t.Fatalf("inserted %q, want fresh value", got)
	}
}

type batchCounter struct {
	r.Source[row, int]
	lookups *int
}

func (b batchCounter) Lookup(ctx context.Context, keys []int) (map[int]row, error) {
	*b.lookups++
	return b.Source.Lookup(ctx, keys)
}

func TestMemoryLimitFlushesEarly(t *testing.T) {
	var old []row
	for i := 0; i < 10; i++ {
		old = append(old, row{i, "v", ""})
	}
	s := memtable.New(key, old, old)
	n := 0
	cfg := config(s, nil)
	cfg.New = batchCounter{s.New(), &n}
	cfg.BatchSize = 100
	cfg.MemoryLimit = 30
	cfg.RowSize = func(row) int64 { return 10 }
	cfg.SkipExtraCheck = true
	run(t, cfg)
	if n != 4 { // 3+3+3+1 rows per batch
		t.Fatalf("lookups=%d", n)
	}
}

type grouped struct{ *memtable.Store[row, int] }

func (grouped) Group(k int) string { return map[bool]string{true: "even", false: "odd"}[k%2 == 0] }

func TestTransactionsSplitByGroupAndSize(t *testing.T) {
	var old []row
	for i := 0; i < 10; i++ {
		old = append(old, row{i, "v", ""})
	}
	s := memtable.New(key, old, nil)
	cfg := config(s, nil)
	cfg.Repairer = grouped{s}
	cfg.Policy.Missing = r.Fix
	cfg.BatchSize = 10
	cfg.TxBatchSize = 3
	rep := run(t, cfg)
	if rep.Inserted != 10 || s.Tx != 4 { // 5 even -> 3+2, 5 odd -> 3+2
		t.Fatalf("%+v tx=%d", rep, s.Tx)
	}
}

type conflictOnce struct {
	*memtable.Store[row, int]
	done *bool
}

func (c conflictOnce) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	if !*c.done {
		*c.done = true
		c.Store.Set(false, row{3, "racer", ""}) // someone inserted first
	}
	return c.Store.InTx(ctx, fn)
}

func TestSnapshotConflictFallsBackToRecheck(t *testing.T) {
	s := fixture()
	done := false
	cfg := config(s, nil)
	cfg.Repairer = conflictOnce{s, &done}
	cfg.Policy = r.Policy{Missing: r.Fix, Different: r.Fix}
	cfg.Insert = r.InsertBatchSnapshot
	cfg.BatchSize = 10
	cfg.SkipExtraCheck = true
	run(t, cfg)
	if got := newRows(s)[3].Value; got != "missing" {
		t.Fatalf("got %q: conflict must be rechecked and overwritten from old", got)
	}
}

func TestLateMatchIsNotMissing(t *testing.T) {
	s := fixture()
	s.Set(false, row{3, "missing", ""})
	cfg := config(s, nil)
	cfg.BatchSize = 1
	if rep := run(t, cfg); rep.Missing != 0 {
		t.Fatalf("%+v", rep)
	}
}

type failingLookup struct {
	r.Source[row, int]
	err error
}

func (f failingLookup) Lookup(context.Context, []int) (map[int]row, error) { return nil, f.err }

func TestLookupErrorNeverMeansMissing(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	sentinel := errors.New("partial query")
	cfg.New = failingLookup{s.New(), sentinel}
	cfg.Policy.Missing = r.Fix
	e, _ := r.New(cfg)
	if _, err := e.Run(context.Background()); !errors.Is(err, sentinel) || s.Tx != 0 {
		t.Fatalf("%v tx=%d", err, s.Tx)
	}
}

type failingScan struct{ r.Source[row, int] }

func (failingScan) Scan(_ context.Context, yield func(row) error) error {
	if err := yield(row{99, "partial", ""}); err != nil {
		return err
	}
	return errors.New("scan interrupted")
}

func TestScanFailureDoesNotFlushTail(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	cfg.Old = failingScan{s.Old()}
	e, _ := r.New(cfg)
	if _, err := e.Run(context.Background()); err == nil || s.Tx != 0 {
		t.Fatalf("%v tx=%d", err, s.Tx)
	}
}

func TestCancelAndRepairerError(t *testing.T) {
	s := fixture()
	e, _ := r.New(config(s, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sentinel := errors.New("tx failed")
	cfg := config(s, nil)
	cfg.Repairer = failingRepairer{sentinel}
	e, _ = r.New(cfg)
	if _, err := e.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

type failingRepairer struct{ err error }

func (f failingRepairer) InTx(context.Context, func(r.Tx[row, int]) error) error { return f.err }

type dupScan struct{ r.Source[row, int] }

func (dupScan) Scan(_ context.Context, yield func(row) error) error {
	if err := yield(row{1, "a", ""}); err != nil {
		return err
	}
	return yield(row{1, "a", ""})
}

func TestDuplicateScanKeyIsAnError(t *testing.T) {
	s := fixture()
	cfg := config(s, nil)
	cfg.Old = dupScan{s.Old()}
	e, _ := r.New(cfg)
	if _, err := e.Run(context.Background()); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestInvalidConfig(t *testing.T) {
	if _, err := r.New(r.Config[row, int]{}); err == nil {
		t.Fatal("expected error")
	}
	cfg := config(fixture(), nil)
	cfg.MemoryLimit = 10
	if _, err := r.New(cfg); err == nil {
		t.Fatal("MemoryLimit without RowSize must fail")
	}
}
