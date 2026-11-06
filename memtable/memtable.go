// Package memtable is an in-memory old/new table pair implementing
// reconcile.Source and reconcile.Repairer, for unit tests and examples.
// One mutex is the transaction boundary; writes are staged and applied on
// commit, so a failing transaction leaves no trace. Not for production data.
package memtable

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

type Store[T any, K cmp.Ordered] struct {
	mu       sync.Mutex
	key      func(T) K
	old, new map[K]T

	// Tx counts committed transactions.
	Tx int
}

func New[T any, K cmp.Ordered](key func(T) K, old, new []T) *Store[T, K] {
	s := &Store[T, K]{key: key, old: map[K]T{}, new: map[K]T{}}
	for _, v := range old {
		s.old[key(v)] = v
	}
	for _, v := range new {
		s.new[key(v)] = v
	}
	return s
}

// Old / New expose each side as a reconcile.Source.
func (s *Store[T, K]) Old() reconcile.Source[T, K] { return side[T, K]{s, true} }
func (s *Store[T, K]) New() reconcile.Source[T, K] { return side[T, K]{s, false} }

// Set writes a row directly (simulating outside traffic); Delete removes it.
func (s *Store[T, K]) Set(old bool, v T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.table(old)[s.key(v)] = v
}
func (s *Store[T, K]) Delete(old bool, k K) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.table(old), k)
}

// Rows returns one side sorted by key.
func (s *Store[T, K]) Rows(old bool) []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.table(old)
	keys := sortedKeys(m)
	out := make([]T, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

func (s *Store[T, K]) table(old bool) map[K]T {
	if old {
		return s.old
	}
	return s.new
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

type side[T any, K cmp.Ordered] struct {
	s   *Store[T, K]
	old bool
}

// Scan snapshots the key set (O(N), fine for tests) and yields in key order.
func (d side[T, K]) Scan(ctx context.Context, yield func(T) error) error {
	d.s.mu.Lock()
	keys := sortedKeys(d.s.table(d.old))
	d.s.mu.Unlock()
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.s.mu.Lock()
		v, ok := d.s.table(d.old)[k]
		d.s.mu.Unlock()
		if ok {
			if err := yield(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d side[T, K]) Lookup(ctx context.Context, keys []K) (map[K]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	out := map[K]T{}
	for _, k := range keys {
		if v, ok := d.s.table(d.old)[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// InTx implements reconcile.Repairer.
func (s *Store[T, K]) InTx(ctx context.Context, fn func(reconcile.Tx[T, K]) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &tx[T, K]{s: s, staged: map[K]T{}}
	if err := fn(t); err != nil {
		return err
	}
	for k, v := range t.staged {
		s.new[k] = v
	}
	s.Tx++
	return nil
}

type tx[T any, K cmp.Ordered] struct {
	s      *Store[T, K]
	staged map[K]T
}

func (t *tx[T, K]) get(k K) (T, bool) {
	if v, ok := t.staged[k]; ok {
		return v, true
	}
	v, ok := t.s.new[k]
	return v, ok
}

func (t *tx[T, K]) Lock(_ context.Context, keys []K) (map[K]T, map[K]T, error) {
	old, cur := map[K]T{}, map[K]T{}
	for _, k := range keys {
		if v, ok := t.s.old[k]; ok {
			old[k] = v
		}
		if v, ok := t.get(k); ok {
			cur[k] = v
		}
	}
	return old, cur, nil
}

func (t *tx[T, K]) Insert(_ context.Context, rows []T) error {
	for _, v := range rows {
		k := t.s.key(v)
		if _, ok := t.get(k); ok {
			return fmt.Errorf("insert %v: %w", k, reconcile.ErrConflict)
		}
		t.staged[k] = v
	}
	return nil
}

func (t *tx[T, K]) Update(_ context.Context, v T) error {
	k := t.s.key(v)
	if _, ok := t.get(k); !ok {
		return fmt.Errorf("update %v: row not found", k)
	}
	t.staged[k] = v
	return nil
}
