package provider

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

const (
	maxAttempts = 3
	// maxRetryAfter caps a server-supplied Retry-After so a hostile or
	// buggy header can't stall a run for minutes (adapted from ZERO's
	// providerio retry policy).
	maxRetryAfter = 30 * time.Second
)

// WithRetry wraps p with exponential backoff + jitter on retryable API
// errors (429, 5xx, Anthropic overloaded), honoring Retry-After. Max 3
// attempts; context cancellation is respected during waits. onRetry, when
// non-nil, is invoked once per re-attempt (stats accounting).
func WithRetry(p Provider, log *slog.Logger, onRetry func()) Provider {
	return &retrier{inner: p, log: log, base: time.Second, onRetry: onRetry}
}

type retrier struct {
	inner   Provider
	log     *slog.Logger
	base    time.Duration // first backoff delay; tests shrink it
	onRetry func()
}

func (r *retrier) Name() string { return r.inner.Name() }

func (r *retrier) Complete(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := r.backoff(attempt, lastErr)
			r.log.Debug("retrying provider call", "provider", r.inner.Name(), "attempt", attempt, "delay", delay, "cause", lastErr)
			if err := sleepCtx(ctx, delay); err != nil {
				return Response{}, err
			}
			if r.onRetry != nil {
				r.onRetry()
			}
		}
		resp, err := r.inner.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.Retryable() || ctx.Err() != nil {
			return Response{}, err
		}
	}
	return Response{}, lastErr
}

func (r *retrier) backoff(attempt int, lastErr error) time.Duration {
	var apiErr *APIError
	if errors.As(lastErr, &apiErr) && apiErr.RetryAfter > 0 {
		return min(apiErr.RetryAfter, maxRetryAfter)
	}
	base := r.base << (attempt - 2)                     // 1s, 2s
	jitter := time.Duration(rand.Int64N(int64(r.base))) //nolint:gosec // jitter, not crypto
	return base + jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
