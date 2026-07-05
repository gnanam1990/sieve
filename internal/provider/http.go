package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// maxErrorBody bounds how much of an error response we read; pattern
// adapted from ZERO's providerio (bounded 64KB error-body read).
const maxErrorBody = 64 * 1024

// APIError is a non-2xx response from a provider API.
type APIError struct {
	Status     int
	Type       string // provider error type, e.g. anthropic "overloaded_error"
	Message    string
	RetryAfter time.Duration // 0 when the header was absent
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("provider API returned %d", e.Status)
	if e.Type != "" {
		msg += " (" + e.Type + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Retryable reports whether the request is safe and worth re-issuing:
// 429, any 5xx (including Anthropic's 529), or an explicit
// overloaded_error type.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500 || e.Type == "overloaded_error"
}

// PostJSON posts payload to url and decodes the 200 response into out.
// Non-2xx responses become *APIError with the provider's error type and
// message extracted from the standard {"error": {"type", "message"}}
// envelope (both Anthropic and OpenAI-compatible APIs use this shape).
func PostJSON(ctx context.Context, hc *http.Client, url string, headers map[string]string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := &APIError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		var envelope struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			apiErr.Type = envelope.Error.Type
			apiErr.Message = envelope.Error.Message
			if apiErr.Message == "" {
				apiErr.Message = envelope.Message
			}
		}
		return apiErr
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// parseRetryAfter accepts both delay-seconds and HTTP-date forms (pattern
// adapted from ZERO's providerio.RetryAfter).
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
