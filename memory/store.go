// Package memory is a runnable transaction example, not a database adapter.
package memory

import (
	"context"
	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
	"sort"
	"sync"
)

type Row struct {
	Identifier int
	Value      string
}
type Records struct{}

func (Records) Key(v Row) int       { return v.Identifier }
func (Records) Equal(a, b Row) bool { return a == b }

type Event struct {
	Key      int
	Kind     r.Kind
	Repaired bool
}
type Store struct {
	mu       sync.Mutex
	Old, New map[int]Row
	Events   []Event
}
type Side struct {
	Store *Store
	Old   bool
}

func (s Side) rows() map[int]Row {
	if s.Old {
		return s.Store.Old
	}
	return s.Store.New
}
func (s Side) Lookup(ctx context.Context, keys []int) (map[int]Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.Store.mu.Lock()
	defer s.Store.mu.Unlock()
	out := make(map[int]Row)
	for _, k := range keys {
		if v, ok := s.rows()[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s Side) Scan(ctx context.Context, yield func(Row) error) error {
	// Demo only: snapshot keys costs O(N). Production adapters should keyset-page.
	s.Store.mu.Lock()
	keys := make([]int, 0, len(s.rows()))
	for k := range s.rows() {
		keys = append(keys, k)
	}
	s.Store.mu.Unlock()
	sort.Ints(keys)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.Store.mu.Lock()
		v, ok := s.rows()[k]
		s.Store.mu.Unlock()
		if ok {
			if err := yield(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) Review(ctx context.Context, c r.Candidate[Row, int], mode r.Mode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// This mutex is the example's transaction boundary: reread both, compare,
	// alert and optionally write atomically. Never trust candidate snapshots.
	old, hasOld := s.Old[c.Key]
	new, hasNew := s.New[c.Key]
	var kind r.Kind
	switch {
	case !hasOld && hasNew:
		kind = r.Extra
	case hasOld && !hasNew:
		kind = r.Missing
	case hasOld && hasNew && old != new:
		kind = r.Different
	default:
		return nil
	}
	fixed := mode == r.Repair && hasOld
	if fixed {
		s.New[c.Key] = old
	}
	s.Events = append(s.Events, Event{Key: c.Key, Kind: kind, Repaired: fixed})
	return nil
}
