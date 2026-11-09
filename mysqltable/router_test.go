package mysqltable

import (
	"strings"
	"testing"
)

// RouterFunc(tables, nil) used to build a Router that panics on the first key:
// the nil check happened in the middle of a reconciliation run, under lock.
// NewPair must reject it up front instead.
func TestNewPairRejectsNilRouteFunc(t *testing.T) {
	c := pairConfig()
	c.Old = RouterFunc[key]([]string{"orders_old"}, nil)
	_, err := NewPair(c)
	if err == nil {
		t.Fatal("expected an error for a nil route function")
	}
	if !strings.Contains(err.Error(), "nil route function") {
		t.Fatalf("error should name the nil route function, got: %v", err)
	}

	// The same guard must apply to the new side.
	c = pairConfig()
	c.New = RouterFunc[key]([]string{"orders_new_00"}, nil)
	if _, err := NewPair(c); err == nil || !strings.Contains(err.Error(), "nil route function") {
		t.Fatalf("new side: expected the nil-route error, got: %v", err)
	}
}

// A duplicated table name makes the scan visit the same table twice and lets one
// batch write it twice; an empty list scans nothing while claiming success.
func TestNewPairRejectsBadTableLists(t *testing.T) {
	cases := []struct {
		name string
		old  Router[key]
		want string
	}{
		{"duplicate", RouterFunc[key]([]string{"orders_old", "orders_old"}, func(key) string { return "orders_old" }), "duplicate table"},
		{"empty", RouterFunc[key](nil, func(key) string { return "orders_old" }), "lists no tables"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := pairConfig()
			c.Old = tc.old
			_, err := NewPair(c)
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error should contain %q, got: %v", tc.name, tc.want, err)
			}
		})
	}
}

// A valid RouterFunc must still be accepted (no regression on the supported path).
func TestNewPairAcceptsRouterFunc(t *testing.T) {
	c := pairConfig()
	c.Old = RouterFunc[key]([]string{"orders_old"}, func(key) string { return "orders_old" })
	if _, err := NewPair(c); err != nil {
		t.Fatalf("valid RouterFunc rejected: %v", err)
	}
}
