package reconcile

import (
	"context"
	"errors"
)

// Source is one logical side of the comparison: the old table, or the new
// table (a sharded new table is still ONE Source covering every shard).
type Source[T any, K comparable] interface {
	// Scan calls yield synchronously once per row, each key exactly once.
	// It returns nil only after a complete traversal and must return yield's
	// error unchanged. Page size / snapshot bounds are the implementation's job.
	Scan(ctx context.Context, yield func(T) error) error

	// Lookup returns every requested key that exists. A key missing from the
	// map means "confirmed absent"; a partial or failed query MUST return an
	// error, never a smaller map.
	Lookup(ctx context.Context, keys []K) (map[K]T, error)
}

// Repairer owns the transaction boundary used to recheck and fix candidates.
type Repairer[T any, K comparable] interface {
	// InTx runs fn in one transaction and commits when fn returns nil. It may
	// re-run fn after a transient failure (deadlock); fn is safe to re-run.
	InTx(ctx context.Context, fn func(Tx[T, K]) error) error
}

// Tx is the work available inside one Repairer transaction.
type Tx[T any, K comparable] interface {
	// Lock rereads keys on BOTH sides with row locks, old side first (the same
	// order your online dual-write code must use), and returns present rows.
	Lock(ctx context.Context, keys []K) (old, new map[K]T, err error)
	// Insert writes rows (old-side content) into the new side. A duplicate
	// key must return an error wrapping ErrConflict.
	Insert(ctx context.Context, rows []T) error
	// Update overwrites the new-side row that has row's key with row.
	Update(ctx context.Context, row T) error
}

// Grouper is optional. When the Repairer implements it, keys sharing one
// transaction always share Group(key) — e.g. one transaction per target shard.
type Grouper[K comparable] interface {
	Group(key K) string
}

// ErrConflict marks a write that lost a race (e.g. duplicate key on insert).
// The engine rereads the affected keys and decides again.
var ErrConflict = errors.New("reconcile: write conflict")

// Comparator decides whether an old row and its new copy are consistent.
type Comparator[T any] interface {
	// Diff returns the names of fields that differ in a way that matters.
	// Empty means consistent; tolerated differences must not be returned.
	Diff(old, new T) []string
}

// Kind classifies a confirmed inconsistency.
type Kind string

const (
	Missing   Kind = "missing"   // in old, not in new
	Different Kind = "different" // in both, Comparator reports fields
	Extra     Kind = "extra"     // only in new; never deleted, only warned
)

// Action is what to do with a confirmed inconsistency.
type Action uint8

const (
	Warn Action = iota // notify only, never write (zero value: safe default)
	Fix                // write old-side content into the new side
)

func (a Action) String() string {
	if a == Fix {
		return "fix"
	}
	return "warn"
}

// Policy chooses Warn or Fix per kind. Extra rows are always warned.
type Policy struct {
	Missing   Action
	Different Action
}

// InsertMode decides how missing rows are inserted when Policy.Missing is Fix.
type InsertMode uint8

const (
	// InsertBatchLocked (default): per transaction, Lock-reread up to
	// TxBatchSize keys, then one multi-row Insert of the fresh old rows.
	InsertBatchLocked InsertMode = iota
	// InsertPerRow: one transaction per missing key. Slowest.
	InsertPerRow
	// InsertBatchSnapshot: multi-row Insert of the rows as scanned, no reread.
	// Fastest. A row updated between scan and insert lands stale in new; the
	// next Run sees it as Different and fixes it under lock. Only safe when
	// old rows are never deleted, and new must not serve reads until a later
	// Run reports Consistent.
	InsertBatchSnapshot
)

func (m InsertMode) String() string {
	switch m {
	case InsertPerRow:
		return "per-row"
	case InsertBatchSnapshot:
		return "batch-snapshot"
	default:
		return "batch-locked"
	}
}

// Finding is one confirmed inconsistency, delivered after its transaction
// committed. Old/New are the rows as reread (nil when absent); for
// InsertBatchSnapshot, Old is the scanned row and New is nil.
type Finding[T any, K comparable] struct {
	Key    K
	Kind   Kind
	Old    *T
	New    *T
	Fields []string // Different only: fields reported by the Comparator
	Fixed  bool     // true when the new side was written
}

// Notifier receives every confirmed Finding (warned and fixed). An error
// stops the Run; the transaction is already committed.
type Notifier[T any, K comparable] interface {
	Notify(ctx context.Context, f Finding[T, K]) error
}
