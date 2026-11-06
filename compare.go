package reconcile

import (
	"bytes"
	"fmt"
	"reflect"
)

// CompareFunc adapts a function to Comparator.
type CompareFunc[T any] func(old, new T) []string

func (f CompareFunc[T]) Diff(old, new T) []string { return f(old, new) }

// Exact treats rows as consistent only when old == new. Beware: == on a
// time.Time compares *Location and the monotonic clock, not the instant; use
// Struct for rows with time fields.
func Exact[T comparable]() Comparator[T] {
	return CompareFunc[T](func(old, new T) []string {
		if old == new {
			return nil
		}
		return []string{"*"}
	})
}

// Field compares one named field. Equal returns true when the values are
// acceptable as consistent (exactly equal, or within a tolerance).
type Field[T any] struct {
	Name  string
	Equal func(old, new T) bool
}

// Eq compares a field exactly.
func Eq[T any, V comparable](name string, get func(T) V) Field[T] {
	return Field[T]{Name: name, Equal: func(a, b T) bool { return get(a) == get(b) }}
}

// Within tolerates |old-new| <= tolerance without int64 subtraction overflow.
// It panics at construction if tolerance is negative.
func Within[T any](name string, get func(T) int64, tolerance int64) Field[T] {
	if tolerance < 0 {
		panic("reconcile.Within: tolerance must be >= 0")
	}
	return Field[T]{Name: name, Equal: func(a, b T) bool {
		x, y := get(a), get(b)
		var distance uint64
		if x >= y {
			distance = uint64(x) - uint64(y)
		} else {
			distance = uint64(y) - uint64(x)
		}
		return distance <= uint64(tolerance)
	}}
}

// Fields compares exactly the listed fields; anything not listed may differ.
// Prefer Struct when you want "compare everything except ...".
func Fields[T any](fields ...Field[T]) Comparator[T] {
	return CompareFunc[T](func(old, new T) []string {
		var diff []string
		for _, f := range fields {
			if !f.Equal(old, new) {
				diff = append(diff, f.Name)
			}
		}
		return diff
	})
}

// Struct compares every exported field of struct T, except the field names
// in ignore (allowed to differ). Per field: a type with an Equal(T) bool
// method (time.Time: same instant, regardless of Location or monotonic
// clock) uses it; []byte treats nil and empty as equal; anything else uses
// reflect.DeepEqual. A Field in custom
// replaces the default comparison of the struct field with the same Name
// (e.g. Within for a timestamp). Unknown names are an error, so a typo can
// never silently disable a comparison. Names must refer to top-level fields;
// promoted fields of anonymous embeddings are rejected. Ignore the whole
// embedding or use Fields with explicit getters for promoted fields.
func Struct[T any](ignore []string, custom ...Field[T]) (Comparator[T], error) {
	rt := reflect.TypeOf((*T)(nil)).Elem()
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("Struct: %v is not a struct", rt)
	}
	skip := map[string]bool{}
	for _, n := range ignore {
		sf, ok := rt.FieldByName(n)
		if !ok {
			return nil, fmt.Errorf("Struct: unknown field %q in ignore", n)
		}
		if len(sf.Index) != 1 {
			return nil, fmt.Errorf("Struct: %q must name a top-level field; promoted fields are not supported", n)
		}
		skip[n] = true
	}
	over := map[string]Field[T]{}
	for _, f := range custom {
		sf, ok := rt.FieldByName(f.Name)
		if !ok || f.Equal == nil {
			return nil, fmt.Errorf("Struct: invalid custom field %q", f.Name)
		}
		if len(sf.Index) != 1 {
			return nil, fmt.Errorf("Struct: %q must name a top-level field; promoted fields are not supported", f.Name)
		}
		over[f.Name] = f
	}
	type field struct {
		name   string
		index  int
		custom func(T, T) bool
		equal  func(a, b reflect.Value) bool
	}
	var fields []field
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if skip[sf.Name] {
			continue
		}
		if !sf.IsExported() {
			return nil, fmt.Errorf("Struct: unexported field %q must be ignored or the type must use Fields", sf.Name)
		}
		fields = append(fields, field{sf.Name, i, over[sf.Name].Equal, valueEqual(sf.Type)})
	}
	return CompareFunc[T](func(old, new T) []string {
		var diff []string
		a, b := reflect.ValueOf(&old).Elem(), reflect.ValueOf(&new).Elem()
		for _, f := range fields {
			var eq bool
			if f.custom != nil {
				eq = f.custom(old, new)
			} else {
				eq = f.equal(a.Field(f.index), b.Field(f.index))
			}
			if !eq {
				diff = append(diff, f.name)
			}
		}
		return diff
	}), nil
}

// valueEqual picks how two values of type t are compared.
func valueEqual(t reflect.Type) func(a, b reflect.Value) bool {
	if m, ok := t.MethodByName("Equal"); ok && m.Type.NumIn() == 2 && m.Type.In(1) == t &&
		m.Type.NumOut() == 1 && m.Type.Out(0).Kind() == reflect.Bool {
		fn := m.Func // resolved once, not per row
		return func(a, b reflect.Value) bool { return fn.Call([]reflect.Value{a, b})[0].Bool() }
	}
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		return func(a, b reflect.Value) bool { return bytes.Equal(a.Bytes(), b.Bytes()) }
	}
	return func(a, b reflect.Value) bool { return reflect.DeepEqual(a.Interface(), b.Interface()) }
}
