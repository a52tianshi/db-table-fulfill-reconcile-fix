package reconcile_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

func collect(cfg *r.Config[row, int]) *[]r.Progress {
	var got []r.Progress
	cfg.Progress = func(p r.Progress) { got = append(got, p) }
	return &got
}

func TestProgressSequence(t *testing.T) {
	s := memtable.New(key, tenRows(), tenRows()[:4]) // 6 missing
	cfg := config(s)
	cfg.Policy, cfg.ScanPageSize, cfg.BatchSize = fix, 3, 4
	got := collect(&cfg)
	rep := mustRun(t, cfg)

	// old: 10 rows -> batches of 4,4,2 (+ phase done); new: 10 rows after fix
	// -> 4,4,2 (+ phase done); then Done.
	if len(*got) != 9 {
		t.Fatalf("%d callbacks: %+v", len(*got), *got)
	}
	want := []struct {
		phase     r.Phase
		batch     int
		phaseDone bool
	}{{r.ScanOld, 1, false}, {r.ScanOld, 2, false}, {r.ScanOld, 3, false}, {r.ScanOld, 3, true},
		{r.ScanNew, 1, false}, {r.ScanNew, 2, false}, {r.ScanNew, 3, false}, {r.ScanNew, 3, true}}
	for i, w := range want {
		p := (*got)[i]
		if p.Phase != w.phase || p.Batch != w.batch || p.PhaseDone != w.phaseDone || p.Done {
			t.Errorf("callback %d: %+v, want %+v", i, p, w)
		}
	}
	last := (*got)[8]
	if !last.Done || last.Err != nil || last.Report.Inserted != rep.Inserted || last.Pages != 4 {
		t.Fatalf("last %+v", last)
	}
	// Counts only grow; after the first batch its 4 rows are committed.
	if first := (*got)[0]; first.Report.OldScanned != 4 || first.Report.Inserted != 0 || first.Pages != 2 {
		t.Fatalf("first %+v", first) // rows 0-3 already exist in new
	}
	for i := 1; i < len(*got); i++ {
		a, b := (*got)[i-1].Report, (*got)[i].Report
		if b.OldScanned < a.OldScanned || b.Inserted < a.Inserted || (*got)[i].Elapsed < (*got)[i-1].Elapsed {
			t.Fatalf("counts went backwards at %d", i)
		}
	}
}

func TestProgressReportsFailure(t *testing.T) {
	s := fixture()
	cfg := config(s)
	cfg.Repairer = failingRepairer{errors.New("tx failed")}
	got := collect(&cfg)
	if _, err := r.Run(context.Background(), cfg); err == nil {
		t.Fatal("expected error")
	}
	last := (*got)[len(*got)-1]
	if !last.Done || last.Err == nil || !strings.Contains(last.Err.Error(), "tx failed") {
		t.Fatalf("%+v", last)
	}
}

type failingRepairer struct{ err error }

func (f failingRepairer) InTx(context.Context, func(r.Tx[row, int]) error) error { return f.err }

func TestInvalidConfigGetsNoProgress(t *testing.T) {
	cfg := config(fixture())
	got := collect(&cfg)
	cfg.BatchSize = 0
	if _, err := r.Run(context.Background(), cfg); err == nil || len(*got) != 0 {
		t.Fatalf("err=%v callbacks=%d", err, len(*got))
	}
}

func TestLogProgressThrottles(t *testing.T) {
	var buf bytes.Buffer
	log := r.LogProgress(&buf, time.Hour)
	for i := 1; i <= 5; i++ {
		log(r.Progress{Phase: r.ScanOld, Batch: i})
	}
	log(r.Progress{Phase: r.ScanOld, Batch: 5, PhaseDone: true})
	log(r.Progress{Phase: r.ScanNew, Batch: 1})
	log(r.Progress{Phase: r.ScanNew, Batch: 1, Done: true})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// first call, phase done, done; the rest throttled
	if len(lines) != 3 || !strings.Contains(lines[0], "batch=1 ") || !strings.Contains(lines[1], "phase done") || !strings.Contains(lines[2], "| done") {
		t.Fatalf("%q", lines)
	}
}
