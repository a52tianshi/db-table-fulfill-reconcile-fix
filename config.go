package reconcile

import (
	"errors"
	"fmt"
	"time"
)

// Config is everything one Run needs. EVERY field is required; there are no
// defaults. Run validates the whole Config before touching any data and
// reports all problems at once.
type Config[T any, K comparable] struct {
	// --- data sides and row identity ---
	Old Source[T, K] // authoritative side
	New Source[T, K] // side being filled / fixed
	Key func(T) K    // stable unique identifier of a row

	// --- decisions ---
	Compare    Comparator[T]  // which fields must match: Exact, Struct, Fields, CompareFunc
	Policy     Policy         // Warn or Fix, per Missing / Different
	Insert     InsertMode     // how missing rows are inserted (also required with Warn)
	ExtraCheck ExtraCheck     // CheckExtra or SkipExtra
	Repairer   Repairer[T, K] // transactions for recheck and fix
	Notifier   Notifier[T, K] // receives every confirmed finding
	// Progress is called synchronously after every completed batch, at each
	// phase end, and once at the end (Done). Use NoProgress to opt out, or
	// LogProgress for a throttled log line. Keep it fast: it blocks the Run.
	Progress func(Progress)

	// --- sizes ---
	ScanPageSize int // rows per ScanPage call, > 0
	BatchSize    int // scanned rows paired per Lookup, > 0
	TxBatchSize  int // max keys per recheck / insert transaction, > 0
	// MemoryLimit flushes the batch early once buffered rows reach this many
	// bytes (as estimated by RowSize). NoMemoryLimit disables it; then
	// RowSize must be nil. The scan page and batch are separately bounded by
	// ScanPageSize and BatchSize; lookup/transaction results add memory.
	// This is not a process RSS cap; a single oversized row is still processed.
	MemoryLimit int64
	RowSize     func(T) int64

	// --- failure handling ---
	Retry Retry // applied to every ScanPage, Lookup, transaction and Notify
}

// Retry is applied to each step separately. When a step still fails after
// MaxAttempts, Run stops and returns that error.
type Retry struct {
	MaxAttempts int           // total attempts per step, >= 1 (1 = no retry)
	Backoff     time.Duration // wait before the 2nd attempt, > 0; doubles each time
	MaxBackoff  time.Duration // cap for the wait, >= Backoff
	// Retryable decides which errors are worth another attempt, e.g.
	// mysqltable.IsTransient, or func(error) bool { return true }.
	// context cancellation and ErrConflict are never retried.
	Retryable func(error) bool
}

func (c *Config[T, K]) validate() error {
	var errs []error
	check := func(ok bool, format string, a ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, a...))
		}
	}
	check(c.Old != nil, "Old is required")
	check(c.New != nil, "New is required")
	check(c.Key != nil, "Key is required")
	check(c.Compare != nil, "Compare is required")
	check(c.Repairer != nil, "Repairer is required")
	check(c.Notifier != nil, "Notifier is required")
	check(c.Progress != nil, "Progress is required (use reconcile.NoProgress to opt out)")
	check(c.Policy.Missing == Warn || c.Policy.Missing == Fix, "Policy.Missing must be Warn or Fix, got %v", c.Policy.Missing)
	check(c.Policy.Different == Warn || c.Policy.Different == Fix, "Policy.Different must be Warn or Fix, got %v", c.Policy.Different)
	check(c.Insert >= InsertBatchLocked && c.Insert <= InsertBatchSnapshot, "Insert must be InsertBatchLocked, InsertPerRow or InsertBatchSnapshot, got %v", c.Insert)
	// A stale snapshot insert is only corrected later as Different; with
	// Different=Warn it would stay stale forever.
	check(!(c.Insert == InsertBatchSnapshot && c.Policy.Missing == Fix && c.Policy.Different != Fix),
		"InsertBatchSnapshot with Policy.Missing=Fix requires Policy.Different=Fix (stale snapshot rows are fixed as Different)")
	check(c.ExtraCheck == CheckExtra || c.ExtraCheck == SkipExtra, "ExtraCheck must be CheckExtra or SkipExtra")
	check(c.ScanPageSize > 0, "ScanPageSize must be > 0, got %d", c.ScanPageSize)
	check(c.BatchSize > 0, "BatchSize must be > 0, got %d", c.BatchSize)
	check(c.TxBatchSize > 0, "TxBatchSize must be > 0, got %d", c.TxBatchSize)
	switch {
	case c.MemoryLimit == NoMemoryLimit:
		check(c.RowSize == nil, "RowSize must be nil when MemoryLimit is NoMemoryLimit")
	case c.MemoryLimit > 0:
		check(c.RowSize != nil, "RowSize is required when MemoryLimit > 0")
	default:
		check(false, "MemoryLimit must be > 0 or NoMemoryLimit, got %d", c.MemoryLimit)
	}
	r := c.Retry
	check(r.MaxAttempts >= 1, "Retry.MaxAttempts must be >= 1, got %d", r.MaxAttempts)
	check(r.Backoff > 0, "Retry.Backoff must be > 0, got %v", r.Backoff)
	check(r.MaxBackoff >= r.Backoff, "Retry.MaxBackoff must be >= Retry.Backoff, got %v", r.MaxBackoff)
	check(r.Retryable != nil, "Retry.Retryable is required")
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("reconcile: invalid config:\n%w", err)
	}
	return nil
}
