package reconcile

import (
	"context"
	"fmt"
	"io"
)

// NotifyFunc adapts a function to Notifier.
type NotifyFunc[T any, K comparable] func(context.Context, Finding[T, K]) error

func (f NotifyFunc[T, K]) Notify(ctx context.Context, x Finding[T, K]) error { return f(ctx, x) }

// LogNotifier writes one line per warned finding; set Fixed to log fixes too.
type LogNotifier[T any, K comparable] struct {
	W     io.Writer
	Fixed bool
}

func (l LogNotifier[T, K]) Notify(_ context.Context, f Finding[T, K]) error {
	if f.Fixed && !l.Fixed {
		return nil
	}
	action := "WARN"
	if f.Fixed {
		action = "FIXED"
	}
	_, err := fmt.Fprintf(l.W, "reconcile %s %s key=%v fields=%v\n", action, f.Kind, f.Key, f.Fields)
	return err
}
