package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

// Records implements reconcile.Records: compare every column.
type Records struct{}

func (Records) Key(o Order) Key       { return KeyOf(o) }
func (Records) Equal(a, b Order) bool { return a == b }

// keysetScan pages one table by primary key up to the high-water mark taken at
// scan start. Each page is fully read and the cursor closed before yield runs,
// so yield (which triggers Lookup / Review) never holds a scan connection.
func keysetScan(ctx context.Context, db *sql.DB, table string, page int, yield func(Order) error) error {
	var hw sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(order_id) FROM "+table).Scan(&hw); err != nil {
		return err
	}
	if !hw.Valid {
		return nil
	}
	q := "SELECT " + Cols + " FROM " + table + " WHERE order_id > ? AND order_id <= ? ORDER BY order_id LIMIT ?"
	last := int64(-1 << 63)
	for {
		rows, err := QueryOrders(ctx, db, q, last, hw.Int64, page)
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		for _, o := range rows {
			if err := yield(o); err != nil {
				return err
			}
		}
		if len(rows) < page {
			return nil
		}
		last = rows[len(rows)-1].OrderID
	}
}

func lookupIn(ctx context.Context, db *sql.DB, table string, keys []Key, out map[Key]Order) error {
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k.OrderID
	}
	rows, err := QueryOrders(ctx, db, "SELECT "+Cols+" FROM "+table+" WHERE order_id IN ("+Placeholders(len(keys))+")", args...)
	if err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	for _, o := range rows {
		// A user_id mismatch yields a key the engine did not ask for -> loud error.
		out[KeyOf(o)] = o
	}
	return nil
}

type OldSource struct {
	DB   *sql.DB
	Page int
}

func (s OldSource) Scan(ctx context.Context, yield func(Order) error) error {
	return keysetScan(ctx, s.DB, OldTable, s.Page, yield)
}
func (s OldSource) Lookup(ctx context.Context, keys []Key) (map[Key]Order, error) {
	out := make(map[Key]Order, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	return out, lookupIn(ctx, s.DB, OldTable, keys, out)
}

// NewSource treats the 100 shard tables as one logical table.
type NewSource struct {
	DB   *sql.DB
	Page int
}

func (s NewSource) Scan(ctx context.Context, yield func(Order) error) error {
	for i := 0; i < Shards; i++ {
		if err := keysetScan(ctx, s.DB, ShardTable(i), s.Page, yield); err != nil {
			return err
		}
	}
	return nil
}
func (s NewSource) Lookup(ctx context.Context, keys []Key) (map[Key]Order, error) {
	out := make(map[Key]Order, len(keys))
	byShard := map[int64][]Key{}
	for _, k := range keys {
		byShard[k.UserID%Shards] = append(byShard[k.UserID%Shards], k)
	}
	for shard, ks := range byShard {
		if err := lookupIn(ctx, s.DB, ShardTable(int(shard)), ks, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Reviewer rereads both sides with row locks (old first, then new: the same
// order the dual writer uses), reclassifies, and repairs from old in one tx.
type Reviewer struct {
	DB  *sql.DB
	Log io.Writer

	// Counted only after commit, from the fresh (locked) state.
	Missing, Different, Extra, Repaired, Stale, Retries atomic.Int64
}

func (v *Reviewer) Review(ctx context.Context, c r.Candidate[Order, Key], mode r.Mode) error {
	for attempt := 0; ; attempt++ {
		err := v.once(ctx, c, mode)
		if err == nil || !Retryable(err) || attempt >= 10 {
			return err
		}
		v.Retries.Add(1)
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
}

func lockOne(ctx context.Context, tx *sql.Tx, table string, id int64) (Order, bool, error) {
	o, err := ScanOrder(tx.QueryRowContext(ctx, "SELECT "+Cols+" FROM "+table+" WHERE order_id=? FOR UPDATE", id))
	if err == sql.ErrNoRows {
		return o, false, nil
	}
	return o, err == nil, err
}

func (v *Reviewer) once(ctx context.Context, c r.Candidate[Order, Key], mode r.Mode) error {
	tx, err := v.DB.BeginTx(ctx, TxOpts())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	newTable := NewTable(c.Key.UserID)
	old, hasOld, err := lockOne(ctx, tx, OldTable, c.Key.OrderID)
	if err != nil {
		return err
	}
	cur, hasNew, err := lockOne(ctx, tx, newTable, c.Key.OrderID)
	if err != nil {
		return err
	}
	if (hasOld && old.UserID != c.Key.UserID) || (hasNew && cur.UserID != c.Key.UserID) {
		return fmt.Errorf("immutable user_id changed for %v", c.Key)
	}
	var counter *atomic.Int64
	repaired := false
	switch {
	case !hasOld && !hasNew, hasOld && hasNew && old == cur:
		counter = &v.Stale // stale candidate, already consistent
	case !hasOld:
		counter = &v.Extra // only in new: alert, never delete
		fmt.Fprintf(v.Log, "ALERT extra row in %s: %v\n", newTable, c.Key)
	case !hasNew:
		counter = &v.Missing
		if mode == r.Repair {
			_, err = tx.ExecContext(ctx, "INSERT INTO "+newTable+" ("+Cols+") VALUES ("+Placeholders(7)+")", old.Args()...)
			repaired = true
		}
	default:
		counter = &v.Different
		if mode == r.Repair {
			_, err = tx.ExecContext(ctx, "UPDATE "+newTable+" SET "+UpdateSet, old.MutableArgs()...)
			repaired = true
		}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	counter.Add(1)
	if repaired {
		v.Repaired.Add(1)
	}
	return nil
}

// BatchReviewer adds a batched Missing path on top of Reviewer:
//
//	Locked=true : per shard, one tx locks+rereads old rows (PK order, same lock
//	              order as the writer), confirms absence in new, inserts fresh values.
//	Locked=false: per shard, one multi-row INSERT of the scanned snapshot. Fast,
//	              but a row updated between scan and insert lands stale in new;
//	              the next pass sees it as Different and Review fixes it.
//
// Anything that is no longer missing falls back to the single-row Review.
type BatchReviewer struct {
	*Reviewer
	Locked bool
}

func (v BatchReviewer) ReviewMissing(ctx context.Context, cs []r.Candidate[Order, Key], mode r.Mode) error {
	byShard := map[string][]r.Candidate[Order, Key]{}
	for _, c := range cs {
		t := NewTable(c.Key.UserID)
		byShard[t] = append(byShard[t], c)
	}
	for table, group := range byShard {
		var fallback []r.Candidate[Order, Key]
		var err error
		for attempt := 0; ; attempt++ {
			if v.Locked {
				fallback, err = v.lockedInsert(ctx, table, group)
			} else {
				fallback, err = v.snapshotInsert(ctx, table, group)
			}
			if err == nil || !Retryable(err) || attempt >= 10 {
				break
			}
			v.Retries.Add(1)
			time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		for _, c := range fallback {
			if err := v.Review(ctx, c, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertRows(ctx context.Context, tx *sql.Tx, table string, rows []Order) error {
	if len(rows) == 0 {
		return nil
	}
	args := make([]any, 0, len(rows)*7)
	for _, o := range rows {
		args = append(args, o.Args()...)
	}
	vals := Placeholders(len(rows))
	vals = strings.ReplaceAll(vals, "?", "("+Placeholders(7)+")")
	_, err := tx.ExecContext(ctx, "INSERT INTO "+table+" ("+Cols+") VALUES "+vals, args...)
	return err
}

func (v BatchReviewer) lockedInsert(ctx context.Context, table string, group []r.Candidate[Order, Key]) ([]r.Candidate[Order, Key], error) {
	tx, err := v.DB.BeginTx(ctx, TxOpts())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]any, len(group))
	for i, c := range group {
		ids[i] = c.Key.OrderID
	}
	in := " WHERE order_id IN (" + Placeholders(len(ids)) + ") ORDER BY order_id FOR UPDATE"
	olds, err := QueryOrders(ctx, tx, "SELECT "+Cols+" FROM "+OldTable+in, ids...)
	if err != nil {
		return nil, err
	}
	news, err := QueryOrders(ctx, tx, "SELECT "+Cols+" FROM "+table+in, ids...)
	if err != nil {
		return nil, err
	}
	fresh := make(map[int64]Order, len(olds))
	for _, o := range olds {
		fresh[o.OrderID] = o
	}
	present := make(map[int64]bool, len(news))
	for _, o := range news {
		present[o.OrderID] = true
	}
	var ins []Order
	var fallback []r.Candidate[Order, Key]
	for _, c := range group {
		o, ok := fresh[c.Key.OrderID]
		if ok && o.UserID == c.Key.UserID && !present[c.Key.OrderID] {
			ins = append(ins, o)
		} else {
			fallback = append(fallback, c) // gone, changed or already present
		}
	}
	if err := insertRows(ctx, tx, table, ins); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	v.Missing.Add(int64(len(ins)))
	v.Repaired.Add(int64(len(ins)))
	return fallback, nil
}

func (v BatchReviewer) snapshotInsert(ctx context.Context, table string, group []r.Candidate[Order, Key]) ([]r.Candidate[Order, Key], error) {
	tx, err := v.DB.BeginTx(ctx, TxOpts())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows := make([]Order, len(group))
	for i, c := range group {
		rows[i] = c.Old
	}
	if err := insertRows(ctx, tx, table, rows); err != nil {
		if IsDuplicate(err) {
			return group, nil // someone inserted meanwhile: decide row by row
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	v.Missing.Add(int64(len(rows)))
	v.Repaired.Add(int64(len(rows)))
	return nil, nil
}
