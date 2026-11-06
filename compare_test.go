package reconcile_test

import (
	"slices"
	"testing"

	r "github.com/a52tianshi/db-table-fulfill-reconcile-fix"
)

type order struct {
	ID        int64
	Amount    int64
	Remark    string
	UpdatedAt int64
}

func TestExact(t *testing.T) {
	c := r.Exact[order]()
	if c.Diff(order{ID: 1}, order{ID: 1}) != nil || len(c.Diff(order{ID: 1}, order{ID: 2})) == 0 {
		t.Fatal("Exact")
	}
}

func TestFieldsComparesOnlyListed(t *testing.T) {
	c := r.Fields(
		r.Eq("amount", func(o order) int64 { return o.Amount }),
		r.Within("updated_at", func(o order) int64 { return o.UpdatedAt }, 1000),
	)
	a := order{Amount: 5, Remark: "x", UpdatedAt: 10_000}
	b := order{Amount: 5, Remark: "y", UpdatedAt: 10_900}
	if d := c.Diff(a, b); d != nil {
		t.Fatalf("remark not listed, updated_at within tolerance: %v", d)
	}
	b.UpdatedAt, b.Amount = 20_000, 6
	if d := c.Diff(a, b); !slices.Equal(d, []string{"amount", "updated_at"}) {
		t.Fatal(d)
	}
}

func TestStructIgnoreAndCustom(t *testing.T) {
	c, err := r.Struct([]string{"Remark"},
		r.Within("UpdatedAt", func(o order) int64 { return o.UpdatedAt }, 5))
	if err != nil {
		t.Fatal(err)
	}
	a := order{ID: 1, Amount: 1, Remark: "a", UpdatedAt: 100}
	b := order{ID: 1, Amount: 1, Remark: "b", UpdatedAt: 104}
	if d := c.Diff(a, b); d != nil {
		t.Fatal(d)
	}
	b.Amount = 2
	if d := c.Diff(a, b); !slices.Equal(d, []string{"Amount"}) {
		t.Fatal(d)
	}
}

func TestStructRejectsTypos(t *testing.T) {
	if _, err := r.Struct[order]([]string{"Remarks"}); err == nil {
		t.Fatal("unknown ignore field must fail")
	}
	if _, err := r.Struct[order](nil, r.Field[order]{Name: "Nope", Equal: func(order, order) bool { return true }}); err == nil {
		t.Fatal("unknown custom field must fail")
	}
	if _, err := r.Struct[int](nil); err == nil {
		t.Fatal("non-struct must fail")
	}
}
