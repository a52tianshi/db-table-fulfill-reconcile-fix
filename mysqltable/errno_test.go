package mysqltable

import (
	"errors"
	"fmt"
	"testing"
)

// The numeric code must end at a delimiter. A naive substring match would read
// "Error 10620" as 1062 and retry a permanent error (or treat a duplicate key
// as transient), and would read "Error 12130" as a deadlock.
func TestErrorCodeBoundary(t *testing.T) {
	cases := []struct {
		msg  string
		dup  bool
		trns bool
	}{
		{"Error 1062 (23000): Duplicate entry '1' for key 'PRIMARY'", true, false},
		{"Error 10620 (HY000): something unrelated", false, false},
		{"Error 1213 (40001): Deadlock found", false, true},
		{"Error 12130 (HY000): something unrelated", false, false},
		{"Error 1213:malformed but delimited", false, true},
		{"wrapped: Error 1062 (23000): Duplicate entry", true, false},
	}
	for _, c := range cases {
		err := errors.New(c.msg)
		if got := IsDuplicateKey(err); got != c.dup {
			t.Errorf("IsDuplicateKey(%q) = %v, want %v", c.msg, got, c.dup)
		}
		if got := IsTransient(err); got != c.trns {
			t.Errorf("IsTransient(%q) = %v, want %v", c.msg, got, c.trns)
		}
	}
}

func TestErrorCodeBoundaryNilAndWrapped(t *testing.T) {
	if IsDuplicateKey(nil) || IsTransient(nil) {
		t.Fatal("nil error must not classify as duplicate or transient")
	}
	wrapped := fmt.Errorf("insert k1: %w", errors.New("Error 1062 (23000): Duplicate entry '1'"))
	if !IsDuplicateKey(wrapped) {
		t.Fatal("wrapped duplicate key must still be detected")
	}
}

func TestErrorClassifierIgnoresPayloadSubstringsAndPermanentWrappers(t *testing.T) {
	cases := []struct {
		name string
		err  error
		dup  bool
		trns bool
	}{
		{
			name: "duplicate key whose key value contains deadlock errno",
			err:  errors.New("Error 1062 (23000): Duplicate entry 'Error 1213: fake deadlock' for key 'PRIMARY'"),
			dup:  true,
			trns: false,
		},
		{
			name: "duplicate key whose key value contains network substring",
			err:  errors.New("Error 1062 (23000): Duplicate entry 'i/o timeout' for key 'PRIMARY'"),
			dup:  true,
			trns: false,
		},
		{
			name: "missing table whose name contains network substring",
			err:  errors.New("Error 1146 (42S02): Table 'db.unexpected EOF' doesn't exist"),
			dup:  false,
			trns: false,
		},
		{
			name: "lock wait timeout whose payload mentions Error 1062",
			err:  errors.New("Error 1205 (HY000): Lock wait timeout exceeded; context 'Error 1062: dup'"),
			dup:  false,
			trns: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDuplicateKey(tc.err); got != tc.dup {
				t.Errorf("IsDuplicateKey(%v) = %v, want %v", tc.err, got, tc.dup)
			}
			if got := IsTransient(tc.err); got != tc.trns {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.trns)
			}
		})
	}
}
