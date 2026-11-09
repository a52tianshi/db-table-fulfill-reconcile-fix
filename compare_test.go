package reconcile_test

import (
	"math"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

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

// Values read back from a database often differ only in representation.
func TestStructComparesValuesNotRepresentation(t *testing.T) {
	type rec struct {
		At   time.Time
		Blob []byte
	}
	c, err := r.Struct[rec](nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now() // carries a monotonic reading
	same := []struct{ a, b rec }{
		{rec{At: now}, rec{At: now.Round(0)}},                         // monotonic stripped
		{rec{At: now.UTC()}, rec{At: now.In(time.FixedZone("X", 0))}}, // other *Location
		{rec{Blob: nil}, rec{Blob: []byte{}}},                         // NULL vs empty
	}
	for i, x := range same {
		if d := c.Diff(x.a, x.b); d != nil {
			t.Errorf("case %d: %v", i, d)
		}
	}
	if d := c.Diff(rec{At: now}, rec{At: now.Add(time.Second)}); !slices.Equal(d, []string{"At"}) {
		t.Fatal(d)
	}
	if d := c.Diff(rec{Blob: []byte("a")}, rec{Blob: []byte("b")}); !slices.Equal(d, []string{"Blob"}) {
		t.Fatal(d)
	}
}

func TestWithinInt64Boundaries(t *testing.T) {
	tests := []struct {
		a, b, tolerance int64
		want            bool
	}{
		{math.MinInt64, math.MaxInt64, 1, false}, {math.MaxInt64, math.MinInt64, 1, false},
		{math.MinInt64, 0, math.MaxInt64, false}, {math.MinInt64, -1, math.MaxInt64, true},
		{-2, 2, 4, true}, {-2, 2, 3, false}, {math.MinInt64, math.MinInt64, 0, true},
		{math.MaxInt64 - 1, math.MaxInt64, 1, true}, {0, 0, 0, true},
	}
	for _, tt := range tests {
		f := r.Within("value", func(v int64) int64 { return v }, tt.tolerance)
		if got := f.Equal(tt.a, tt.b); got != tt.want {
			t.Errorf("Within(%d,%d,tolerance=%d)=%v want %v", tt.a, tt.b, tt.tolerance, got, tt.want)
		}
	}
}

func TestWithinRejectsNegativeTolerance(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("negative tolerance must panic at construction")
		}
	}()
	r.Within("value", func(v int64) int64 { return v }, -1)
}

func FuzzWithin(f *testing.F) {
	f.Add(int64(math.MinInt64), int64(math.MaxInt64), int64(1))
	f.Add(int64(-2), int64(2), int64(4))
	f.Add(int64(0), int64(0), int64(0))
	f.Fuzz(func(t *testing.T, a, b, tolerance int64) {
		tolerance &= math.MaxInt64
		distance := new(big.Int).Sub(big.NewInt(a), big.NewInt(b))
		distance.Abs(distance)
		want := distance.Cmp(big.NewInt(tolerance)) <= 0
		got := r.Within("value", func(v int64) int64 { return v }, tolerance).Equal(a, b)
		if got != want {
			t.Fatalf("a=%d b=%d tolerance=%d got=%v want=%v", a, b, tolerance, got, want)
		}
	})
}

func TestStructRejectsPromotedFieldConfiguration(t *testing.T) {
	type Embedded struct{ Timestamp int64 }
	type rec struct {
		ID int
		Embedded
	}
	type ptrRec struct {
		ID int
		*Embedded
	}
	never := func(ptrRec, ptrRec) bool { t.Fatal("must reject before comparing"); return false }
	cases := map[string]func() error{
		"ignore value": func() error { _, err := r.Struct[rec]([]string{"Timestamp"}); return err },
		"custom value": func() error {
			_, err := r.Struct[rec](nil, r.Within("Timestamp", func(v rec) int64 { return v.Timestamp }, 5))
			return err
		},
		"ignore pointer": func() error { _, err := r.Struct[ptrRec]([]string{"Timestamp"}); return err },
		"custom pointer": func() error {
			_, err := r.Struct[ptrRec](nil, r.Field[ptrRec]{Name: "Timestamp", Equal: never})
			return err
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if err := build(); err == nil || !strings.Contains(err.Error(), "top-level") {
				t.Fatalf("expected explicit top-level field error, got %v", err)
			}
		})
	}
	cmp, err := r.Struct[rec]([]string{"Embedded"})
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(rec{ID: 1, Embedded: Embedded{1}}, rec{ID: 1, Embedded: Embedded{2}}); len(d) > 0 {
		t.Fatal(d)
	}
}

// A field listed in both ignore and custom used to have its comparator silently
// dropped: the field was skipped and reported equal forever.
func TestStructRejectsIgnoreAndCustomOverlap(t *testing.T) {
	if _, err := r.Struct[order]([]string{"Amount"}, r.Field[order]{Name: "Amount", Equal: func(a, b order) bool { return true }}); err == nil {
		t.Fatal("expected an error when a field is in both ignore and custom")
	}
	if _, err := r.Struct[order](nil,
		r.Field[order]{Name: "ID", Equal: func(a, b order) bool { return true }},
		r.Field[order]{Name: "ID", Equal: func(a, b order) bool { return false }},
	); err == nil {
		t.Fatal("expected an error for a duplicated custom field")
	}
}
