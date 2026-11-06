package memtable_test

import (
	"context"
	"errors"
	"testing"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable"
)

func id(v int) int { return v }

func TestFailedTxLeavesNoTrace(t *testing.T) {
	s := memtable.New(id, []int{1, 2}, nil)
	boom := errors.New("boom")
	err := s.InTx(context.Background(), func(tx r.Tx[int, int]) error {
		if err := tx.Insert(context.Background(), []int{1}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || len(s.Rows(false)) != 0 || s.Tx != 0 {
		t.Fatalf("%v %v", err, s.Rows(false))
	}
}

func TestInsertDuplicateIsConflict(t *testing.T) {
	s := memtable.New(id, []int{1}, []int{1})
	err := s.InTx(context.Background(), func(tx r.Tx[int, int]) error {
		return tx.Insert(context.Background(), []int{1})
	})
	if !errors.Is(err, r.ErrConflict) {
		t.Fatal(err)
	}
}

func TestLockSeesStagedWrites(t *testing.T) {
	s := memtable.New(id, []int{1}, nil)
	err := s.InTx(context.Background(), func(tx r.Tx[int, int]) error {
		_ = tx.Insert(context.Background(), []int{1})
		_, cur, _ := tx.Lock(context.Background(), []int{1})
		if _, ok := cur[1]; !ok {
			return errors.New("staged insert not visible")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
