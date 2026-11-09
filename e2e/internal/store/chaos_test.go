package store_test

import (
	"context"
	"errors"
	"testing"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store"
	"github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable"
)

type stubSource struct{}

func (stubSource) ScanPage(context.Context, r.Cursor, int) ([]store.Order, r.Cursor, error) {
	return nil, nil, nil
}

func (stubSource) Lookup(context.Context, []store.Key) (map[store.Key]store.Order, error) {
	return map[store.Key]store.Order{}, nil
}

type stubRepairer struct{}

func (stubRepairer) Group(k store.Key) string { return store.NewTable(k.UserID) }
func (stubRepairer) InTx(_ context.Context, fn func(r.Tx[store.Order, store.Key]) error) error {
	return fn(nil)
}

func TestChaosZeroRatePassesThrough(t *testing.T) {
	c := store.NewChaos(0, 42)
	src := c.Source(stubSource{})
	rep := c.Repairer(stubRepairer{})
	not := c.Notifier(r.NotifyFunc[store.Order, store.Key](func(context.Context, r.Finding[store.Order, store.Key]) error { return nil }))

	if _, _, err := src.ScanPage(context.Background(), nil, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Lookup(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := rep.InTx(context.Background(), func(r.Tx[store.Order, store.Key]) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := not.Notify(context.Background(), r.Finding[store.Order, store.Key]{}); err != nil {
		t.Fatal(err)
	}
	if c.Injected() != 0 || c.Stats().Total() != 0 {
		t.Fatalf("expected 0 injected faults, got %d (%+v)", c.Injected(), c.Stats())
	}
}

func TestChaosInjectsTransientErrorsAcrossAllStages(t *testing.T) {
	c := store.NewChaos(1.0, 99)
	src := c.Source(stubSource{})
	rep := c.Repairer(stubRepairer{})
	not := c.Notifier(r.NotifyFunc[store.Order, store.Key](func(context.Context, r.Finding[store.Order, store.Key]) error { return nil }))

	for i := 0; i < 20; i++ {
		_, _, err := src.ScanPage(context.Background(), nil, 10)
		if !errors.Is(err, store.ErrInjected) || !mysqltable.IsTransient(err) {
			t.Fatalf("ScanPage err=%v is not transient ErrInjected", err)
		}
	}
	if _, err := src.Lookup(context.Background(), nil); !errors.Is(err, store.ErrInjected) || !mysqltable.IsTransient(err) {
		t.Fatalf("Lookup err=%v", err)
	}
	if err := rep.InTx(context.Background(), func(r.Tx[store.Order, store.Key]) error { return nil }); !errors.Is(err, store.ErrInjected) || !mysqltable.IsTransient(err) {
		t.Fatalf("InTx err=%v", err)
	}
	if err := not.Notify(context.Background(), r.Finding[store.Order, store.Key]{}); !errors.Is(err, store.ErrInjected) || !mysqltable.IsTransient(err) {
		t.Fatalf("Notify err=%v", err)
	}

	st := c.Stats()
	if st.Scan != 20 || st.Lookup != 1 || st.TxBegin != 1 || st.Notify != 1 || st.Total() != c.Injected() {
		t.Fatalf("stats=%+v injected=%d", st, c.Injected())
	}
}
