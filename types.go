package reconcile

import (
	"context"
	"errors"
)

// Cursor is an opaque scan position owned by a Source. nil means "start"
// when passed in, and "no more pages" when returned.
type Cursor any

// Source is one logical side of the comparison: the old table, or the new
// table (a sharded new table is still ONE Source covering every shard).
// Implementations make ONE attempt per call; the engine retries.
type Source[T any, K comparable] interface {
	// ScanPage returns up to limit rows after cursor, and the cursor of the
	// next page (nil when the scan is complete). A page may be empty while
	// next is non-nil (e.g. an empty shard). Calling it again with the same
	// cursor must return the same position (retries rely on it). Across the
	// whole scan, every key appears exactly once.
	ScanPage(ctx context.Context, cursor Cursor, limit int) (rows []T, next Cursor, err error)

	// Lookup returns every requested key that exists. A key missing from the
	// map means "confirmed absent"; a partial or failed query MUST return an
	// error, never a smaller map.
	Lookup(ctx context.Context, keys []K) (map[K]T, error)
}

// Repairer owns the transaction boundary used to recheck and fix candidates.
type Repairer[T any, K comparable] interface {
	// InTx runs fn in one transaction and commits when fn returns nil; any
	// error rolls back. ONE attempt: the engine re-runs the whole InTx on
	// retryable errors. fn rereads under lock, so re-running is safe even
	// when a commit's outcome was unknown.
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
// It is never retried as-is: the engine rereads the keys and decides again.
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

// Action is what to do with a confirmed inconsistency. The zero value is
// invalid: every action must be chosen explicitly.
type Action uint8

const (
	Warn Action = iota + 1 // notify only, never write
	Fix                    // write old-side content into the new side
)

func (a Action) String() string {
	switch a {
	case Warn:
		return "warn"
	case Fix:
		return "fix"
	}
	return "unset"
}

// Policy chooses Warn or Fix per kind. Extra rows are always warned.
type Policy struct {
	Missing   Action
	Different Action
}

// InsertMode decides how missing rows are inserted when Policy.Missing is
// Fix. The zero value is invalid.
type InsertMode uint8

const (
	// InsertBatchLocked: per transaction, Lock-reread up to TxBatchSize keys,
	// then one multi-row Insert of the fresh old rows.
	InsertBatchLocked InsertMode = iota + 1
	// InsertPerRow: one transaction per key that the initial comparison
	// reported as missing. Candidates that showed up as different are still
	// batched by TxBatchSize. Slowest.
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
	case InsertBatchLocked:
		return "batch-locked"
	case InsertPerRow:
		return "per-row"
	case InsertBatchSnapshot:
		return "batch-snapshot"
	}
	return "unset"
}

// ExtraCheck decides whether the reverse pass (scan New, look up Old) runs
// to find rows that exist only in New. The zero value is invalid.
type ExtraCheck uint8

const (
	CheckExtra ExtraCheck = iota + 1
	SkipExtra
)

// NoMemoryLimit disables the byte limit (rows are still bounded by
// BatchSize). Config.MemoryLimit must be this or a positive byte count.
const NoMemoryLimit int64 = -1

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

// Notifier receives every confirmed Finding (warned and fixed). It is
// called serially after the WHOLE committed batch is counted. It is retried
// like any other step; exhausting retries stops the Run and leaves later
// findings undelivered. Notifications may be duplicated or lost; there is no
// durable queue/outbox or replay guarantee on a later Run.
type Notifier[T any, K comparable] interface {
	Notify(ctx context.Context, f Finding[T, K]) error
}
