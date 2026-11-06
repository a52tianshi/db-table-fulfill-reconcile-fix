package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Run validates cfg, then does one forward pass (scan Old, look up New:
// missing / different) and, with CheckExtra, one reverse pass (scan New,
// look up Old: extra). Every external call is retried per cfg.Retry; when a
// step exhausts its attempts, or any non-retryable error occurs, Run stops
// and returns it. Transactions already committed stay committed.
//
// Run keeps no state between calls and is safe to call repeatedly.
func Run[T any, K comparable](ctx context.Context, cfg Config[T, K]) (rep Report, err error) {
	if err := cfg.validate(); err != nil {
		return rep, err
	}
	start := time.Now()
	defer func() { rep.Duration = time.Since(start) }()
	r := &run[T, K]{cfg: cfg, ctx: ctx, rep: &rep}
	r.grouper, _ = cfg.Repairer.(Grouper[K])
	if err = r.pass(false); err != nil {
		return rep, err
	}
	if cfg.ExtraCheck == CheckExtra {
		if err = r.pass(true); err != nil {
			return rep, err
		}
	}
	return rep, ctx.Err()
}

type run[T any, K comparable] struct {
	cfg     Config[T, K]
	grouper Grouper[K]
	ctx     context.Context
	rep     *Report
}

// buffer holds one batch of scanned rows; it never evicts.
type buffer[T any, K comparable] struct {
	rows  []T
	keys  []K
	seen  map[K]struct{}
	bytes int64
}

func (b *buffer[T, K]) reset() {
	clear(b.seen)
	b.rows, b.keys, b.bytes = b.rows[:0], b.keys[:0], 0
}

func (r *run[T, K]) pass(reverse bool) error {
	cfg := &r.cfg
	src, dst, side := cfg.Old, cfg.New, "old"
	counter := &r.rep.OldScanned
	if reverse {
		src, dst, side, counter = cfg.New, cfg.Old, "new", &r.rep.NewScanned
	}
	b := &buffer[T, K]{seen: make(map[K]struct{}, cfg.BatchSize)}
	flush := func() error {
		if len(b.keys) == 0 {
			return nil
		}
		if err := r.flush(dst, b, reverse); err != nil {
			return err
		}
		b.reset()
		return nil
	}
	var cursor Cursor
	for page := 1; ; page++ {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		var rows []T
		var next Cursor
		err := r.do(fmt.Sprintf("scan %s page %d", side, page), func() (err error) {
			rows, next, err = src.ScanPage(r.ctx, cursor, cfg.ScanPageSize)
			return err
		})
		if err != nil {
			return err // the unfinished tail batch is never judged
		}
		if len(rows) > cfg.ScanPageSize {
			return fmt.Errorf("scan %s page %d: %d rows exceed ScanPageSize %d", side, page, len(rows), cfg.ScanPageSize)
		}
		for _, v := range rows {
			var size int64
			if cfg.MemoryLimit > 0 {
				size = cfg.RowSize(v)
				if len(b.keys) > 0 && b.bytes+size > cfg.MemoryLimit {
					if err := flush(); err != nil {
						return err
					}
				}
			}
			k := cfg.Key(v)
			if _, dup := b.seen[k]; dup {
				return fmt.Errorf("scan %s: duplicate key %v in one batch: ScanPage must emit each key once", side, k)
			}
			b.seen[k] = struct{}{}
			b.rows, b.keys, b.bytes = append(b.rows, v), append(b.keys, k), b.bytes+size
			*counter++
			if len(b.keys) >= cfg.BatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if next == nil {
			return flush()
		}
		cursor = next
	}
}

// flush pairs one batch with a complete Lookup and handles candidates.
func (r *run[T, K]) flush(dst Source[T, K], b *buffer[T, K], reverse bool) error {
	var found map[K]T
	err := r.do(fmt.Sprintf("lookup %d keys", len(b.keys)), func() (err error) {
		found, err = dst.Lookup(r.ctx, b.keys)
		return err
	})
	if err != nil {
		return err
	}
	for k, v := range found {
		if _, ok := b.seen[k]; !ok || r.cfg.Key(v) != k {
			return fmt.Errorf("lookup returned unrequested key %v", k)
		}
	}
	var missing []int
	var recheck []K
	for i, k := range b.keys {
		other, ok := found[k]
		switch {
		case reverse && !ok:
			recheck = append(recheck, k) // extra candidate
		case reverse:
		case !ok:
			missing = append(missing, i)
		case len(r.cfg.Compare.Diff(b.rows[i], other)) > 0:
			recheck = append(recheck, k)
		}
	}
	r.rep.Suspected += int64(len(missing) + len(recheck))
	if err := r.recheck(recheck); err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if r.cfg.Policy.Missing == Fix && r.cfg.Insert == InsertBatchSnapshot {
		rows := make([]T, len(missing))
		for j, i := range missing {
			rows[j] = b.rows[i]
		}
		return r.snapshotInsert(rows)
	}
	keys := make([]K, len(missing))
	for j, i := range missing {
		keys[j] = b.keys[i]
	}
	if r.cfg.Policy.Missing == Fix && r.cfg.Insert == InsertPerRow {
		for _, k := range keys {
			if err := r.recheck([]K{k}); err != nil {
				return err
			}
		}
		return nil
	}
	return r.recheck(keys)
}

// chunks splits keys into transactions: same Group, at most TxBatchSize.
func (r *run[T, K]) chunks(keys []K) [][]int {
	groups := map[string][]int{}
	var names []string
	for i, k := range keys {
		g := ""
		if r.grouper != nil {
			g = r.grouper.Group(k)
		}
		if _, ok := groups[g]; !ok {
			names = append(names, g)
		}
		groups[g] = append(groups[g], i)
	}
	sort.Strings(names)
	var out [][]int
	for _, g := range names {
		idx := groups[g]
		for len(idx) > 0 {
			n := min(len(idx), r.cfg.TxBatchSize)
			out = append(out, idx[:n])
			idx = idx[n:]
		}
	}
	return out
}

const maxConflictRounds = 3

// recheck rereads keys under lock, classifies them from the fresh state and
// applies Policy, one transaction per chunk.
func (r *run[T, K]) recheck(keys []K) error {
	for _, idx := range r.chunks(keys) {
		chunk := make([]K, len(idx))
		for j, i := range idx {
			chunk[j] = keys[i]
		}
		var err error
		for round := 0; ; round++ {
			err = r.recheckTx(chunk)
			if !errors.Is(err, ErrConflict) || round >= maxConflictRounds {
				break
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *run[T, K]) recheckTx(keys []K) error {
	cfg := &r.cfg
	var findings []Finding[T, K]
	var stale int64
	err := r.do(fmt.Sprintf("recheck tx (%d keys)", len(keys)), func() error {
		return cfg.Repairer.InTx(r.ctx, func(tx Tx[T, K]) error {
			findings, stale = findings[:0], 0 // the transaction may be re-run
			old, cur, err := tx.Lock(r.ctx, keys)
			if err != nil {
				return err
			}
			if err := r.checkKeys(keys, old, cur); err != nil {
				return permanent{err}
			}
			var inserts []T
			for _, k := range keys {
				o, hasOld := old[k]
				n, hasNew := cur[k]
				f := Finding[T, K]{Key: k}
				if hasOld {
					f.Old = &o
				}
				if hasNew {
					f.New = &n
				}
				switch {
				case !hasOld && !hasNew:
					stale++
					continue
				case !hasOld:
					f.Kind = Extra
				case !hasNew:
					f.Kind = Missing
					if cfg.Policy.Missing == Fix {
						inserts = append(inserts, o)
						f.Fixed = true
					}
				default:
					if f.Fields = cfg.Compare.Diff(o, n); len(f.Fields) == 0 {
						stale++
						continue
					}
					f.Kind = Different
					if cfg.Policy.Different == Fix {
						if err := tx.Update(r.ctx, o); err != nil {
							return err
						}
						f.Fixed = true
					}
				}
				findings = append(findings, f)
			}
			if len(inserts) > 0 {
				return tx.Insert(r.ctx, inserts)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	r.rep.Stale += stale
	return r.record(findings)
}

func (r *run[T, K]) checkKeys(keys []K, maps ...map[K]T) error {
	want := make(map[K]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	for _, m := range maps {
		for k, v := range m {
			if _, ok := want[k]; !ok || r.cfg.Key(v) != k {
				return fmt.Errorf("lock returned unrequested key %v (immutable key fields changed?)", k)
			}
		}
	}
	return nil
}

// snapshotInsert writes scanned rows without reread; a conflict falls back
// to a locked recheck of that chunk.
func (r *run[T, K]) snapshotInsert(rows []T) error {
	keys := make([]K, len(rows))
	for i, v := range rows {
		keys[i] = r.cfg.Key(v)
	}
	for _, idx := range r.chunks(keys) {
		chunk := make([]T, len(idx))
		ck := make([]K, len(idx))
		for j, i := range idx {
			chunk[j], ck[j] = rows[i], keys[i]
		}
		err := r.do(fmt.Sprintf("snapshot insert tx (%d rows)", len(chunk)), func() error {
			return r.cfg.Repairer.InTx(r.ctx, func(tx Tx[T, K]) error { return tx.Insert(r.ctx, chunk) })
		})
		if errors.Is(err, ErrConflict) {
			if err := r.recheck(ck); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		fs := make([]Finding[T, K], len(chunk))
		for j := range chunk {
			fs[j] = Finding[T, K]{Key: ck[j], Kind: Missing, Old: &chunk[j], Fixed: true}
		}
		if err := r.record(fs); err != nil {
			return err
		}
	}
	return nil
}

// record counts and notifies committed findings.
func (r *run[T, K]) record(fs []Finding[T, K]) error {
	for _, f := range fs {
		switch f.Kind {
		case Missing:
			r.rep.Missing++
		case Different:
			r.rep.Different++
		case Extra:
			r.rep.Extra++
		}
		switch {
		case f.Fixed && f.Kind == Missing:
			r.rep.Inserted++
		case f.Fixed:
			r.rep.Updated++
		default:
			r.rep.Warned++
		}
		err := r.do(fmt.Sprintf("notify %s %v", f.Kind, f.Key), func() error {
			return r.cfg.Notifier.Notify(r.ctx, f)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
