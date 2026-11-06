package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// permanent marks an error raised by the engine's own checks inside a step
// (a broken invariant, not a flaky call): never retried.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

func (r *run[T, K]) retryable(err error) bool {
	var p permanent
	switch {
	case errors.As(err, &p), errors.Is(err, ErrConflict),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	}
	return r.cfg.Retry.Retryable(err)
}

// do runs one step with the configured Retry policy.
func (r *run[T, K]) do(step string, f func() error) error {
	policy := r.cfg.Retry
	wait := policy.Backoff
	for attempt := 1; ; attempt++ {
		err := f()
		if err == nil {
			return nil
		}
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", step, ctxErr)
		}
		if !r.retryable(err) {
			return fmt.Errorf("%s: %w", step, err)
		}
		if attempt >= policy.MaxAttempts {
			return fmt.Errorf("%s: giving up after %d attempts: %w", step, attempt, err)
		}
		r.rep.Retries++
		t := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return fmt.Errorf("%s: %w", step, r.ctx.Err())
		case <-t.C:
		}
		wait = min(wait*2, policy.MaxBackoff)
	}
}
