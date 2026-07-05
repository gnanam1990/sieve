package provider

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

type scriptedProvider struct {
	responses []any // error or Response
	calls     int
}

func (s *scriptedProvider) Name() string { return "scripted" }

func (s *scriptedProvider) Complete(_ context.Context, _ Request) (Response, error) {
	item := s.responses[min(s.calls, len(s.responses)-1)]
	s.calls++
	if err, ok := item.(error); ok {
		return Response{}, err
	}
	return item.(Response), nil
}

func newTestRetrier(inner Provider, onRetry func()) *retrier {
	return &retrier{
		inner:   inner,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		base:    time.Millisecond,
		onRetry: onRetry,
	}
}

func TestRetrySucceedsAfterRetryable(t *testing.T) {
	var retries int
	inner := &scriptedProvider{responses: []any{
		&APIError{Status: 529, Type: "overloaded_error"},
		&APIError{Status: 429},
		Response{Text: "ok"},
	}}
	r := newTestRetrier(inner, func() { retries++ })
	resp, err := r.Complete(context.Background(), Request{})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if inner.calls != 3 || retries != 2 {
		t.Fatalf("calls=%d retries=%d, want 3/2", inner.calls, retries)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	inner := &scriptedProvider{responses: []any{&APIError{Status: 500}}}
	r := newTestRetrier(inner, nil)
	_, err := r.Complete(context.Background(), Request{})
	if err == nil {
		t.Fatal("want error after exhausting attempts")
	}
	if inner.calls != maxAttempts {
		t.Fatalf("calls=%d, want %d", inner.calls, maxAttempts)
	}
}

func TestRetryStopsOnNonRetryable(t *testing.T) {
	inner := &scriptedProvider{responses: []any{&APIError{Status: 401, Message: "bad key"}}}
	r := newTestRetrier(inner, nil)
	_, err := r.Complete(context.Background(), Request{})
	if err == nil || inner.calls != 1 {
		t.Fatalf("err=%v calls=%d, want immediate failure", err, inner.calls)
	}
}

func TestRetryStopsOnPlainError(t *testing.T) {
	inner := &scriptedProvider{responses: []any{errors.New("boom")}}
	r := newTestRetrier(inner, nil)
	if _, err := r.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("want error")
	}
	if inner.calls != 1 {
		t.Fatalf("non-API errors must not be retried, got %d calls", inner.calls)
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	inner := &scriptedProvider{responses: []any{&APIError{Status: 429, RetryAfter: time.Hour}}}
	r := newTestRetrier(inner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := r.Complete(ctx, Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("calls=%d, want 1 (cancelled during backoff)", inner.calls)
	}
}

func TestBackoffHonorsRetryAfterCapped(t *testing.T) {
	r := newTestRetrier(nil, nil)
	got := r.backoff(2, &APIError{Status: 429, RetryAfter: 5 * time.Minute})
	if got != maxRetryAfter {
		t.Fatalf("Retry-After must be capped at %v, got %v", maxRetryAfter, got)
	}
	got = r.backoff(2, &APIError{Status: 429, RetryAfter: 2 * time.Second})
	if got != 2*time.Second {
		t.Fatalf("want server-supplied 2s, got %v", got)
	}
}

func TestAPIErrorRetryable(t *testing.T) {
	cases := []struct {
		err  APIError
		want bool
	}{
		{APIError{Status: 429}, true},
		{APIError{Status: 500}, true},
		{APIError{Status: 529}, true},
		{APIError{Status: 503, Type: "overloaded_error"}, true},
		{APIError{Status: 400, Type: "overloaded_error"}, true},
		{APIError{Status: 400}, false},
		{APIError{Status: 401}, false},
		{APIError{Status: 404}, false},
	}
	for _, c := range cases {
		if got := c.err.Retryable(); got != c.want {
			t.Errorf("Retryable(%+v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("7"); d != 7*time.Second {
		t.Fatalf("seconds form: got %v", d)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 || d > 11*time.Second {
		t.Fatalf("http-date form: got %v", d)
	}
	if d := parseRetryAfter("garbage"); d != 0 {
		t.Fatalf("garbage should yield 0, got %v", d)
	}
}
