// Package reconcile discovers candidates; only the business Reviewer may write.
package reconcile

import (
	"context"
	"fmt"
)

type Mode uint8

const (
	AlertOnly Mode = iota
	Repair
)

type Kind string

const (
	Missing   Kind = "missing"
	Different Kind = "different"
	Extra     Kind = "extra"
)

// Source must emit each stable unique identifier exactly once. Scan returns nil
// only after complete traversal. yield is synchronous and must not be retained.
// Lookup must fully resolve every requested key: absence means confirmed absent.
// Partial query results MUST return an error. Returned records must be immutable.
type Source[T any, K comparable] interface {
	Scan(context.Context, func(T) error) error
	Lookup(context.Context, []K) (map[K]T, error)
}
type Records[T any, K comparable] interface {
	Key(T) K
	Equal(T, T) bool
}

// Cache belongs exclusively to one Run. Reset must release retained records.
// Capacity is measured in records, not bytes. Implementations must not evict.
type Cache[T any, K comparable] interface {
	Reset()
	Put(K, T) error
	Get(K) (T, bool)
}
type Candidate[T any, K comparable] struct {
	Key            K
	Kind           Kind
	Old, New       T
	HasOld, HasNew bool
}

// Reviewer MUST reread BOTH sides inside a business-controlled transaction,
// classify again, and alert/repair from the fresh state. AlertOnly never writes;
// Repair inserts/overwrites from old; extra rows are alerted and NEVER deleted.
// A stale candidate that is now equal requires no action. Return only after
// commit/alert completes; errors stop Run, earlier commits are not rolled back.
type Reviewer[T any, K comparable] interface {
	Review(context.Context, Candidate[T, K], Mode) error
}

// MissingBatchReviewer is optional. When the Reviewer also implements it and
// mode is Repair, all Missing candidates of one forward batch are handed over
// in a single call; Different/Extra still go through Review one by one.
// The implementation chooses its safety level: reread old under lock (exact),
// or insert the scanned snapshot (faster; a stale value written this way is
// only corrected by a later pass whose Review rereads). Return only after
// commit; an error stops Run.
type MissingBatchReviewer[T any, K comparable] interface {
	ReviewMissing(context.Context, []Candidate[T, K], Mode) error
}
type Engine[T any, K comparable] struct {
	Old, New  Source[T, K]
	Records   Records[T, K]
	Cache     Cache[T, K]
	Reviewer  Reviewer[T, K]
	BatchSize int
}
type Stats struct{ OldScanned, NewScanned, Candidates int }

func (e Engine[T, K]) Run(ctx context.Context, mode Mode) (s Stats, err error) {
	if e.BatchSize <= 0 || e.Old == nil || e.New == nil || e.Records == nil || e.Cache == nil || e.Reviewer == nil {
		return s, fmt.Errorf("invalid engine configuration")
	}
	if mode != AlertOnly && mode != Repair {
		return s, fmt.Errorf("invalid mode")
	}
	defer e.Cache.Reset()
	review := func(c Candidate[T, K]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.Candidates++
		if err := e.Reviewer.Review(ctx, c, mode); err != nil {
			return fmt.Errorf("review %v (%s): %w", c.Key, c.Kind, err)
		}
		return nil
	}
	batcher, _ := e.Reviewer.(MissingBatchReviewer[T, K])
	if mode != Repair {
		batcher = nil
	}
	var missing []Candidate[T, K]
	// Each batch is fully paired before the scanner is allowed to continue.
	pass := func(source, target Source[T, K], reverse bool) error {
		e.Cache.Reset()
		keys := make([]K, 0, e.BatchSize)
		flush := func() error {
			if len(keys) == 0 {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rows, err := target.Lookup(ctx, keys)
			if err != nil {
				return fmt.Errorf("lookup: %w", err)
			}
			wanted := make(map[K]bool, len(keys))
			for _, k := range keys {
				wanted[k] = true
			}
			for k, v := range rows {
				if !wanted[k] || e.Records.Key(v) != k {
					return fmt.Errorf("lookup returned invalid key %v", k)
				}
			}
			for _, k := range keys {
				if err := ctx.Err(); err != nil {
					return err
				}
				v, ok := e.Cache.Get(k)
				if !ok {
					return fmt.Errorf("cache lost key %v", k)
				}
				other, found := rows[k]
				if reverse {
					if !found {
						if err := review(Candidate[T, K]{Key: k, Kind: Extra, New: v, HasNew: true}); err != nil {
							return err
						}
					}
					continue
				}
				c := Candidate[T, K]{Key: k, Old: v, HasOld: true, New: other, HasNew: found}
				if !found {
					c.Kind = Missing
					if batcher != nil {
						missing = append(missing, c)
						continue
					}
				} else if !e.Records.Equal(v, other) {
					c.Kind = Different
				} else {
					continue
				}
				if err := review(c); err != nil {
					return err
				}
			}
			if len(missing) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				s.Candidates += len(missing)
				if err := batcher.ReviewMissing(ctx, missing, mode); err != nil {
					return fmt.Errorf("review %d missing: %w", len(missing), err)
				}
				missing = missing[:0]
			}
			e.Cache.Reset()
			keys = keys[:0]
			return nil
		}
		err := source.Scan(ctx, func(v T) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			k := e.Records.Key(v)
			if _, ok := e.Cache.Get(k); ok {
				return fmt.Errorf("duplicate batch key %v", k)
			}
			if err := e.Cache.Put(k, v); err != nil {
				return fmt.Errorf("cache: %w", err)
			}
			keys = append(keys, k)
			if reverse {
				s.NewScanned++
			} else {
				s.OldScanned++
			}
			if len(keys) == e.BatchSize {
				return flush()
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		return flush()
	}
	if err = pass(e.Old, e.New, false); err != nil {
		return s, err
	}
	err = pass(e.New, e.Old, true)
	if err == nil {
		err = ctx.Err()
	}
	return s, err
}

type MemoryCache[T any, K comparable] struct {
	Limit int
	rows  map[K]T
}

func (c *MemoryCache[T, K]) Reset()            { c.rows = nil }
func (c *MemoryCache[T, K]) Get(k K) (T, bool) { v, ok := c.rows[k]; return v, ok }
func (c *MemoryCache[T, K]) Put(k K, v T) error {
	if c.rows == nil {
		c.rows = make(map[K]T)
	}
	if _, ok := c.rows[k]; !ok && len(c.rows) >= c.Limit {
		return fmt.Errorf("cache capacity %d exceeded", c.Limit)
	}
	c.rows[k] = v
	return nil
}
