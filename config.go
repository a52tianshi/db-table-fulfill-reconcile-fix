package reconcile

import (
	"errors"
	"os"
)

// Config wires a reconcile run. Required: Old, New, Key, Compare, Repairer.
type Config[T any, K comparable] struct {
	Old Source[T, K] // authoritative side
	New Source[T, K] // side being filled / fixed

	Key      func(T) K      // stable unique identifier of a row
	Compare  Comparator[T]  // see Exact, Fields, Struct
	Repairer Repairer[T, K] // transactions for recheck and fix
	Notifier Notifier[T, K] // default: LogNotifier to stderr (warnings only)
	Policy   Policy         // default: Warn for every kind
	Insert   InsertMode     // default: InsertBatchLocked

	// BatchSize is how many scanned rows are buffered, then paired with ONE
	// Lookup on the other side. Default 1000.
	BatchSize int
	// TxBatchSize caps keys per recheck / insert transaction. Default 200.
	TxBatchSize int
	// MemoryLimit (bytes, optional) flushes the buffer early once the rows in
	// it reach this size. Requires RowSize. 0 = bounded by BatchSize only.
	MemoryLimit int64
	RowSize     func(T) int64

	// SkipExtraCheck skips the reverse pass (scan New, look up Old) that
	// finds rows only present in New.
	SkipExtraCheck bool
}

const (
	DefaultBatchSize   = 1000
	DefaultTxBatchSize = 200
)

func (c *Config[T, K]) normalize() error {
	var errs []error
	req := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}
	req(c.Old != nil, "Old is required")
	req(c.New != nil, "New is required")
	req(c.Key != nil, "Key is required")
	req(c.Compare != nil, "Compare is required")
	req(c.Repairer != nil, "Repairer is required")
	req(c.BatchSize >= 0 && c.TxBatchSize >= 0 && c.MemoryLimit >= 0, "sizes must not be negative")
	req(c.MemoryLimit == 0 || c.RowSize != nil, "MemoryLimit requires RowSize")
	req(c.Policy.Missing <= Fix && c.Policy.Different <= Fix, "invalid Policy action")
	req(c.Insert <= InsertBatchSnapshot, "invalid InsertMode")
	if c.BatchSize == 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.TxBatchSize == 0 {
		c.TxBatchSize = DefaultTxBatchSize
	}
	if c.Notifier == nil {
		c.Notifier = LogNotifier[T, K]{W: os.Stderr}
	}
	return errors.Join(errs...)
}
