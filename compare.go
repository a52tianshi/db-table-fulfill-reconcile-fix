package reconcile

import (
	"fmt"
	"reflect"
)

// CompareFunc adapts a function to Comparator.
type CompareFunc[T any] func(old, new T) []string

func (f CompareFunc[T]) Diff(old, new T) []string { return f(old, new) }

// Exact treats rows as consistent only when old == new.
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

// Within tolerates |old-new| <= tolerance, e.g. timestamps written separately.
func Within[T any](name string, get func(T) int64, tolerance int64) Field[T] {
	return Field[T]{Name: name, Equal: func(a, b T) bool {
		d := get(a) - get(b)
		return -tolerance <= d && d <= tolerance
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

// Struct compares every exported field of struct T with reflect.DeepEqual,
// except the field names in ignore (allowed to differ). A Field in custom
// replaces the default comparison of the struct field with the same Name
// (e.g. Within for a timestamp). Unknown names are an error, so a typo can
// never silently disable a comparison.
func Struct[T any](ignore []string, custom ...Field[T]) (Comparator[T], error) {
	rt := reflect.TypeOf((*T)(nil)).Elem()
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("Struct: %v is not a struct", rt)
	}
	skip := map[string]bool{}
	for _, n := range ignore {
		if _, ok := rt.FieldByName(n); !ok {
			return nil, fmt.Errorf("Struct: unknown field %q in ignore", n)
		}
		skip[n] = true
	}
	over := map[string]Field[T]{}
	for _, f := range custom {
		if _, ok := rt.FieldByName(f.Name); !ok || f.Equal == nil {
			return nil, fmt.Errorf("Struct: invalid custom field %q", f.Name)
		}
		over[f.Name] = f
	}
	type field struct {
		name   string
		index  int
		custom func(T, T) bool
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
		fields = append(fields, field{sf.Name, i, over[sf.Name].Equal})
	}
	return CompareFunc[T](func(old, new T) []string {
		var diff []string
		a, b := reflect.ValueOf(&old).Elem(), reflect.ValueOf(&new).Elem()
		for _, f := range fields {
			var eq bool
			if f.custom != nil {
				eq = f.custom(old, new)
			} else {
				eq = reflect.DeepEqual(a.Field(f.index).Interface(), b.Field(f.index).Interface())
			}
			if !eq {
				diff = append(diff, f.name)
			}
		}
		return diff
	}), nil
}
