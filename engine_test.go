package reconcile_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

var noRetry = r.Retry{MaxAttempts: 1, Backoff: time.Millisecond, MaxBackoff: time.Millisecond,
	Retryable: func(error) bool { return true }}

// config is a complete, valid Config; each test changes only what it needs.
func config(s *memtable.Store[row, int]) r.Config[row, int] {
	return r.Config[row, int]{
		Old: s.Old(), New: s.New(), Key: key,
		Compare:    r.Exact[row](),
		Policy:     r.Policy{Missing: r.Warn, Different: r.Warn},
		Insert:     r.InsertBatchLocked,
		ExtraCheck: r.CheckExtra,
		Repairer:   s,
		Notifier:   r.LogNotifier[row, int]{W: io.Discard},

		ScanPageSize: 2,
		BatchSize:    2,
		TxBatchSize:  200,
		MemoryLimit:  r.NoMemoryLimit,
		Retry:        noRetry,
	}
}

var fix = r.Policy{Missing: r.Fix, Different: r.Fix}

func mustRun(t *testing.T, cfg r.Config[row, int]) r.Report {
	t.Helper()
	rep, err := r.Run(context.Background(), cfg)
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

// ---- policies ----

func TestWarnPolicyNeverWrites(t *testing.T) {
	s := fixture()
	rec := &recorder{}
	cfg := config(s)
	cfg.Notifier = rec
	rep := mustRun(t, cfg)
	if rep.Missing != 1 || rep.Different != 1 || rep.Extra != 1 || rep.Warned != 3 || rep.Inserted+rep.Updated != 0 {
		t.Fatalf("%+v", rep)
	}
	if len(rec.got) != 3 || newRows(s)[2].Value != "stale" {
		t.Fatalf("findings=%+v", rec.got)
	}
}

func TestFixPolicyInsertsOverwritesKeepsExtra(t *testing.T) {
	for _, mode := range []r.InsertMode{r.InsertBatchLocked, r.InsertPerRow, r.InsertBatchSnapshot} {
		t.Run(mode.String(), func(t *testing.T) {
			s := fixture()
			cfg := config(s)
			cfg.Policy, cfg.Insert = fix, mode
			rep := mustRun(t, cfg)
			got := newRows(s)
			if rep.Inserted != 1 || rep.Updated != 1 || rep.Extra != 1 || got[2].Value != "old" || got[3].Value != "missing" || got[4].Value != "extra" {
				t.Fatalf("%+v %+v", rep, got)
			}
			if again := mustRun(t, cfg); again.Missing+again.Different != 0 || again.Extra != 1 {
				t.Fatalf("second run %+v", again)
			}
		})
	}
}

func TestPolicyPerKind(t *testing.T) {
	s := fixture()
	cfg := config(s)
	cfg.Policy = r.Policy{Missing: r.Fix, Different: r.Warn}
	rep := mustRun(t, cfg)
	got := newRows(s)
	if rep.Inserted != 1 || rep.Updated != 0 || got[3].Value != "missing" || got[2].Value != "stale" {
		t.Fatalf("%+v %+v", rep, got)
	}
}

func TestSkipExtra(t *testing.T) {
	cfg := config(fixture())
	cfg.ExtraCheck = r.SkipExtra
	if rep := mustRun(t, cfg); rep.NewScanned != 0 || rep.Extra != 0 {
		t.Fatalf("%+v", rep)
	}
}

// ---- comparison ----

func TestToleratedFieldIsConsistent(t *testing.T) {
	s := memtable.New(key, []row{{1, "v", "a"}}, []row{{1, "v", "b"}})
	cfg := config(s)
	cmp, err := r.Struct[row]([]string{"Note"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Compare, cfg.Policy = cmp, fix
	if rep := mustRun(t, cfg); rep.Suspected != 0 || rep.Updated != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestDifferentFindingCarriesFields(t *testing.T) {
	s := memtable.New(key, []row{{1, "a", "x"}}, []row{{1, "b", "x"}})
	cfg := config(s)
	cfg.Compare, _ = r.Struct[row](nil)
	rec := &recorder{}
	cfg.Notifier = rec
	mustRun(t, cfg)
	if len(rec.got) != 1 || len(rec.got[0].Fields) != 1 || rec.got[0].Fields[0] != "Value" {
		t.Fatalf("%+v", rec.got)
	}
}

// ---- concurrency: recheck from fresh state ----

// hookRepairer runs before() right before each transaction, like online
// traffic arriving between the first comparison and the recheck.
type hookRepairer struct {
	*memtable.Store[row, int]
	before func()
}

func (s hookRepairer) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	s.before()
	return s.Store.InTx(ctx, fn)
}

func TestStaleCandidateIsSkipped(t *testing.T) {
	s := fixture()
	cfg := config(s)
	cfg.Policy, cfg.ExtraCheck = fix, r.SkipExtra
	cfg.BatchSize = 10 // both candidates found before the first transaction
	cfg.Repairer = hookRepairer{s, func() {
		s.Set(false, row{2, "old", ""})     // dual write already fixed it
		s.Set(false, row{3, "missing", ""}) // and inserted it
	}}
	rep := mustRun(t, cfg)
	if rep.Suspected != 2 || rep.Stale != 2 || rep.Missing+rep.Different != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestRecheckUsesFreshOldValue(t *testing.T) {
	s := fixture()
	cfg := config(s)
	cfg.Policy.Missing = r.Fix
	cfg.Repairer = hookRepairer{s, func() { s.Set(true, row{3, "updated", ""}) }}
	mustRun(t, cfg)
	if got := newRows(s)[3].Value; got != "updated" {
		t.Fatalf("inserted %q, want fresh value", got)
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
	cfg := config(s)
	cfg.Repairer = conflictOnce{s, &done}
	cfg.Policy, cfg.Insert, cfg.ExtraCheck = fix, r.InsertBatchSnapshot, r.SkipExtra
	cfg.BatchSize = 10
	mustRun(t, cfg)
	if got := newRows(s)[3].Value; got != "missing" {
		t.Fatalf("got %q: conflict must be rechecked and overwritten from old", got)
	}
}

// ---- batching and memory ----

type lookupCounter struct {
	r.Source[row, int]
	n *int
}

func (b lookupCounter) Lookup(ctx context.Context, keys []int) (map[int]row, error) {
	*b.n++
	return b.Source.Lookup(ctx, keys)
}

func tenRows() []row {
	var rows []row
	for i := 0; i < 10; i++ {
		rows = append(rows, row{i, "v", ""})
	}
	return rows
}

func TestMemoryLimitFlushesEarly(t *testing.T) {
	s := memtable.New(key, tenRows(), tenRows())
	n := 0
	cfg := config(s)
	cfg.New = lookupCounter{s.New(), &n}
	cfg.ExtraCheck, cfg.ScanPageSize, cfg.BatchSize = r.SkipExtra, 4, 100
	cfg.MemoryLimit, cfg.RowSize = 30, func(row) int64 { return 10 }
	mustRun(t, cfg)
	if n != 4 { // 3+3+3+1 rows per batch
		t.Fatalf("lookups=%d", n)
	}
}

func TestBatchSpansPages(t *testing.T) {
	s := memtable.New(key, tenRows(), tenRows())
	n := 0
	cfg := config(s)
	cfg.New = lookupCounter{s.New(), &n}
	cfg.ExtraCheck, cfg.ScanPageSize, cfg.BatchSize = r.SkipExtra, 3, 7
	if rep := mustRun(t, cfg); rep.OldScanned != 10 || n != 2 { // 7 + 3
		t.Fatalf("%+v lookups=%d", rep, n)
	}
}

type grouped struct{ *memtable.Store[row, int] }

func (grouped) Group(k int) string { return map[bool]string{true: "even", false: "odd"}[k%2 == 0] }

func TestTransactionsSplitByGroupAndSize(t *testing.T) {
	s := memtable.New(key, tenRows(), nil)
	cfg := config(s)
	cfg.Repairer, cfg.Policy = grouped{s}, fix
	cfg.ScanPageSize, cfg.BatchSize, cfg.TxBatchSize = 10, 10, 3
	rep := mustRun(t, cfg)
	if rep.Inserted != 10 || s.Tx != 4 { // 5 even -> 3+2, 5 odd -> 3+2
		t.Fatalf("%+v tx=%d", rep, s.Tx)
	}
}

func TestLateMatchIsNotMissing(t *testing.T) {
	s := fixture()
	s.Set(false, row{3, "missing", ""})
	cfg := config(s)
	cfg.ScanPageSize, cfg.BatchSize = 1, 1
	if rep := mustRun(t, cfg); rep.Missing != 0 {
		t.Fatalf("%+v", rep)
	}
}

// ---- failures and retries ----

// flaky fails the first `fail` calls of each method with err.
type flaky struct {
	r.Source[row, int]
	err        error
	fail       int
	scan, look *int
}

func (f flaky) ScanPage(ctx context.Context, c r.Cursor, n int) ([]row, r.Cursor, error) {
	if *f.scan++; *f.scan <= f.fail {
		return nil, nil, f.err
	}
	return f.Source.ScanPage(ctx, c, n)
}

func (f flaky) Lookup(ctx context.Context, keys []int) (map[int]row, error) {
	if *f.look++; *f.look <= f.fail {
		return nil, f.err
	}
	return f.Source.Lookup(ctx, keys)
}

type flakyTx struct {
	*memtable.Store[row, int]
	err   error
	fail  int
	calls *int
}

func (f flakyTx) InTx(ctx context.Context, fn func(r.Tx[row, int]) error) error {
	if *f.calls++; *f.calls <= f.fail {
		return f.err
	}
	return f.Store.InTx(ctx, fn)
}

var errFlaky = errors.New("Error 1213: deadlock")

func retry(attempts int) r.Retry {
	return r.Retry{MaxAttempts: attempts, Backoff: time.Microsecond, MaxBackoff: time.Millisecond,
		Retryable: func(err error) bool { return errors.Is(err, errFlaky) }}
}

func TestEveryStepIsRetried(t *testing.T) {
	s := fixture()
	var oldScan, oldLook, newScan, newLook, tx int
	cfg := config(s)
	cfg.Old = flaky{s.Old(), errFlaky, 2, &oldScan, &oldLook}
	cfg.New = flaky{s.New(), errFlaky, 2, &newScan, &newLook}
	cfg.Repairer = flakyTx{s, errFlaky, 2, &tx}
	cfg.Policy, cfg.Retry = fix, retry(3)
	rep := mustRun(t, cfg)
	if rep.Retries != 10 || rep.Inserted != 1 || rep.Updated != 1 { // 2 per step kind x 5
		t.Fatalf("%+v", rep)
	}
}

func TestRetriesExhaustedStopsRun(t *testing.T) {
	s := fixture()
	var tx int
	cfg := config(s)
	cfg.Repairer = flakyTx{s, errFlaky, 100, &tx}
	cfg.Policy, cfg.Retry = fix, retry(4)
	_, err := r.Run(context.Background(), cfg)
	if !errors.Is(err, errFlaky) || tx != 4 || len(s.Rows(false)) != 3 {
		t.Fatalf("err=%v tx calls=%d", err, tx)
	}
}

func TestNonRetryableErrorStopsImmediately(t *testing.T) {
	s := fixture()
	var scan, look int
	boom := errors.New("Error 1146: table doesn't exist")
	cfg := config(s)
	cfg.Old = flaky{s.Old(), boom, 100, &scan, &look}
	cfg.Retry = retry(5)
	if _, err := r.Run(context.Background(), cfg); !errors.Is(err, boom) || scan != 1 {
		t.Fatalf("err=%v attempts=%d", err, scan)
	}
}

type failingNotifier struct{ calls *int }

func (f failingNotifier) Notify(context.Context, r.Finding[row, int]) error {
	*f.calls++
	return errFlaky
}

func TestNotifierRetriedThenStops(t *testing.T) {
	calls := 0
	cfg := config(fixture())
	cfg.Notifier, cfg.Retry = failingNotifier{&calls}, retry(3)
	if _, err := r.Run(context.Background(), cfg); !errors.Is(err, errFlaky) || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

type failAfterFirstPage struct{ r.Source[row, int] }

func (f failAfterFirstPage) ScanPage(ctx context.Context, c r.Cursor, n int) ([]row, r.Cursor, error) {
	if c != nil {
		return nil, nil, errors.New("scan interrupted")
	}
	return f.Source.ScanPage(ctx, c, n)
}

func TestScanFailureDoesNotJudgeTail(t *testing.T) {
	s := fixture()
	cfg := config(s)
	cfg.Old, cfg.Policy, cfg.BatchSize = failAfterFirstPage{s.Old()}, fix, 10
	if _, err := r.Run(context.Background(), cfg); err == nil || s.Tx != 0 {
		t.Fatalf("%v tx=%d", err, s.Tx)
	}
}

func TestLookupErrorNeverMeansMissing(t *testing.T) {
	s := fixture()
	var scan, look int
	cfg := config(s)
	cfg.New = flaky{s.New(), errors.New("partial query"), 100, &scan, &look}
	cfg.Policy = fix
	if _, err := r.Run(context.Background(), cfg); err == nil || s.Tx != 0 {
		t.Fatalf("%v tx=%d", err, s.Tx)
	}
}

func TestCancelIsNotRetried(t *testing.T) {
	cfg := config(fixture())
	cfg.Retry = r.Retry{MaxAttempts: 100, Backoff: time.Hour, MaxBackoff: time.Hour, Retryable: func(error) bool { return true }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type dupScan struct{ r.Source[row, int] }

func (dupScan) ScanPage(context.Context, r.Cursor, int) ([]row, r.Cursor, error) {
	return []row{{1, "a", ""}, {1, "a", ""}}, nil, nil
}

func TestDuplicateScanKeyIsAnError(t *testing.T) {
	cfg := config(fixture())
	cfg.Old = dupScan{}
	if _, err := r.Run(context.Background(), cfg); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

// ---- strict config ----

func TestEmptyConfigListsEveryProblem(t *testing.T) {
	_, err := r.Run(context.Background(), r.Config[row, int]{})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"Old", "New", "Key", "Compare", "Repairer", "Notifier", "Policy.Missing",
		"Policy.Different", "Insert", "ExtraCheck", "ScanPageSize", "BatchSize", "TxBatchSize", "MemoryLimit",
		"Retry.MaxAttempts", "Retry.Backoff", "Retry.Retryable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestInvalidConfigTouchesNoData(t *testing.T) {
	s := fixture()
	cases := map[string]func(*r.Config[row, int]){
		"zero policy":           func(c *r.Config[row, int]) { c.Policy = r.Policy{} },
		"zero insert":           func(c *r.Config[row, int]) { c.Insert = 0 },
		"zero memory":           func(c *r.Config[row, int]) { c.MemoryLimit = 0 },
		"limit without size":    func(c *r.Config[row, int]) { c.MemoryLimit = 10 },
		"size without limit":    func(c *r.Config[row, int]) { c.RowSize = func(row) int64 { return 1 } },
		"zero attempts":         func(c *r.Config[row, int]) { c.Retry.MaxAttempts = 0 },
		"max backoff too small": func(c *r.Config[row, int]) { c.Retry.MaxBackoff = 0 },
	}
	for name, mutate := range cases {
		cfg := config(s)
		cfg.Policy = fix
		mutate(&cfg)
		if _, err := r.Run(context.Background(), cfg); err == nil || s.Tx != 0 {
			t.Errorf("%s: err=%v tx=%d", name, err, s.Tx)
		}
	}
}
