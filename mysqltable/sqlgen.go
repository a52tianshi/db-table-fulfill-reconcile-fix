package mysqltable

import "strings"

// queries holds the SQL text for one schema; table names are filled per call.
type queries struct {
	cols, key string
	setCols   string // "`a`=?,`b`=?" for every non-key column
	nCols     int
}

func newQueries(columns []string, key string) queries {
	q := queries{key: quote(key), nCols: len(columns)}
	quoted := make([]string, len(columns))
	var set []string
	for i, c := range columns {
		quoted[i] = quote(c)
		if c != key {
			set = append(set, quote(c)+"=?")
		}
	}
	q.cols = strings.Join(quoted, ",")
	q.setCols = strings.Join(set, ",")
	return q
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// last reads the row with the highest key: its typed key is the high-water mark.
func (q queries) last(table string) string {
	return "SELECT " + q.cols + " FROM " + quote(table) + " ORDER BY " + q.key + " DESC LIMIT 1"
}

// page reads the next keyset page up to the high-water mark hw.
func (q queries) page(table string, first bool) string {
	where := q.key + " <= ?"
	if !first {
		where = q.key + " > ? AND " + where
	}
	return "SELECT " + q.cols + " FROM " + quote(table) + " WHERE " + where + " ORDER BY " + q.key + " LIMIT ?"
}

func (q queries) in(table string, n int, lock bool) string {
	s := "SELECT " + q.cols + " FROM " + quote(table) + " WHERE " + q.key + " IN (" + marks(n) + ")"
	if lock {
		s += " ORDER BY " + q.key + " FOR UPDATE"
	}
	return s
}

func (q queries) insert(table string, rows int) string {
	row := "(" + marks(q.nCols) + ")"
	return "INSERT INTO " + quote(table) + " (" + q.cols + ") VALUES " +
		strings.TrimSuffix(strings.Repeat(row+",", rows), ",")
}

func (q queries) update(table string) string {
	return "UPDATE " + quote(table) + " SET " + q.setCols + " WHERE " + q.key + " = ?"
}
