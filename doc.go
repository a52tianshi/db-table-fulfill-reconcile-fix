// Package reconcile compares an authoritative old table with a new table
// (possibly sharded), and warns about or fixes the differences.
//
// For every batch of scanned old rows, the new side is looked up in one call
// and compared. Suspected differences are never acted on directly: they are
// reread inside a transaction (with row locks, old side first), classified
// again from the fresh state, and only then warned or fixed per Policy.
// The one exception is InsertBatchSnapshot: it inserts the rows as scanned
// and falls back to the locking recheck only on a duplicate-key conflict;
// see InsertMode for its preconditions.
// Rows only present in new are warned, never deleted.
//
// The single entry point is Run(ctx, Config). Every Config field is
// required; Run validates all of them before touching any data. You provide
// (or take from package mysqltable):
//
//	Source     old/new ScanPage + batch Lookup (sharding lives here)
//	Repairer   transactions: Lock-reread, Insert (batch), Update (single row)
//	Comparator which fields must match, which may differ (Exact, Fields, Struct)
//	Policy     Warn or Fix per Missing / Different
//	Retry      per-step retry of every scan page, lookup, transaction, notify
//	Progress   callback after every committed batch (or NoProgress)
//
// Source and Repairer implementations make one attempt per call; the engine
// owns retries. Package memtable is an in-memory implementation for tests
// and examples.
package reconcile
