package reconcile_test

import (
	"context"
	"errors"
	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memory"
	"testing"
)

func fixture() (*memory.Store, r.Engine[memory.Row, int]) {
	s := &memory.Store{Old: map[int]memory.Row{1: {Identifier: 1, Value: "same"}, 2: {Identifier: 2, Value: "old"}, 3: {Identifier: 3, Value: "missing"}}, New: map[int]memory.Row{1: {Identifier: 1, Value: "same"}, 2: {Identifier: 2, Value: "new"}, 4: {Identifier: 4, Value: "extra"}}}
	return s, r.Engine[memory.Row, int]{Old: memory.Side{Store: s, Old: true}, New: memory.Side{Store: s}, Records: memory.Records{}, Cache: &r.MemoryCache[memory.Row, int]{Limit: 2}, Reviewer: s, BatchSize: 2}
}
func TestModesAndPreservation(t *testing.T) {
	s, e := fixture()
	stats, err := e.Run(context.Background(), r.AlertOnly)
	if err != nil || stats.Candidates != 3 || len(s.New) != 3 || s.New[2].Value != "new" {
		t.Fatalf("%+v %v %+v", stats, err, s.New)
	}
	_, err = e.Run(context.Background(), r.Repair)
	if err != nil || s.New[2] != s.Old[2] || s.New[3] != s.Old[3] || s.New[4].Value != "extra" {
		t.Fatalf("%v %+v", err, s.New)
	}
}

type reviewer func(context.Context, r.Candidate[memory.Row, int], r.Mode) error

func (f reviewer) Review(c context.Context, v r.Candidate[memory.Row, int], m r.Mode) error {
	return f(c, v, m)
}
func TestFreshReviewDiscardsStaleCandidate(t *testing.T) {
	s, e := fixture()
	e.Reviewer = reviewer(func(ctx context.Context, c r.Candidate[memory.Row, int], m r.Mode) error {
		if c.Key == 2 {
			s.New[2] = s.Old[2]
		}
		return s.Review(ctx, c, m)
	})
	if _, err := e.Run(context.Background(), r.Repair); err != nil {
		t.Fatal(err)
	}
	for _, v := range s.Events {
		if v.Key == 2 {
			t.Fatal("stale candidate applied")
		}
	}
}

type badSource struct {
	r.Source[memory.Row, int]
	err error
}

func (s badSource) Lookup(context.Context, []int) (map[int]memory.Row, error) { return nil, s.err }
func TestIncompleteLookupNeverMeansMissing(t *testing.T) {
	s, e := fixture()
	sentinel := errors.New("partial query")
	e.New = badSource{Source: e.New, err: sentinel}
	_, err := e.Run(context.Background(), r.Repair)
	if !errors.Is(err, sentinel) || len(s.Events) != 0 {
		t.Fatalf("%v %+v", err, s.Events)
	}
}
func TestCancelAndReviewerError(t *testing.T) {
	_, e := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx, r.Repair); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sentinel := errors.New("transaction failed")
	calls := 0
	e.Reviewer = reviewer(func(context.Context, r.Candidate[memory.Row, int], r.Mode) error { calls++; return sentinel })
	if _, err := e.Run(context.Background(), r.Repair); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("%v calls=%d", err, calls)
	}
}
func TestCacheBoundAndInvalidConfig(t *testing.T) {
	_, e := fixture()
	e.Cache = &r.MemoryCache[memory.Row, int]{Limit: 1}
	if _, err := e.Run(context.Background(), r.Repair); err == nil {
		t.Fatal("expected cache bound error")
	}
	e.BatchSize = 0
	if _, err := e.Run(context.Background(), r.Repair); err == nil {
		t.Fatal("expected config error")
	}
}

// A match at a later scan position must be found through complete Lookup.
func TestLateMatchNotMissing(t *testing.T) {
	s, e := fixture()
	s.New[3] = s.Old[3]
	e.BatchSize = 1
	_, err := e.Run(context.Background(), r.AlertOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range s.Events {
		if v.Key == 3 {
			t.Fatal("late match classified missing")
		}
	}
}

type failedScan struct{ r.Source[memory.Row, int] }

func (s failedScan) Scan(ctx context.Context, yield func(memory.Row) error) error {
	if err := yield(memory.Row{Identifier: 99, Value: "partial"}); err != nil {
		return err
	}
	return errors.New("scan interrupted")
}
func TestScanFailureDoesNotFlushTail(t *testing.T) {
	s, e := fixture()
	e.Old = failedScan{e.Old}
	if _, err := e.Run(context.Background(), r.Repair); err == nil || len(s.Events) != 0 {
		t.Fatalf("%v %+v", err, s.Events)
	}
}
func TestReviewerReclassifiesExtra(t *testing.T) {
	s, _ := fixture()
	delete(s.Old, 2)
	err := s.Review(context.Background(), r.Candidate[memory.Row, int]{Key: 2, Kind: r.Different}, r.Repair)
	if err != nil || s.New[2].Value != "new" || len(s.Events) != 1 || s.Events[0].Kind != r.Extra || s.Events[0].Repaired {
		t.Fatalf("%v %+v", err, s.Events)
	}
}
