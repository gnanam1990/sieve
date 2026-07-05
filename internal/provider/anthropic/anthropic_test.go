package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/provider"
)

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("bad headers: %v", r.Header)
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			System    string `json:"system"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.System != "sys" || len(body.Messages) != 1 || body.Messages[0].Content != "hello" || body.MaxTokens != 100 {
			t.Errorf("bad payload: %+v", body)
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"part1 "},{"type":"thinking","thinking":"x"},{"type":"text","text":"part2"}],
			"usage":{"input_tokens":11,"output_tokens":7}}`)
	}))
	defer srv.Close()

	c := New("sk-test", "test-model", srv.URL)
	resp, err := c.Complete(context.Background(), provider.Request{System: "sys", User: "hello", MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "part1 part2" {
		t.Fatalf("text %q: non-text blocks must be skipped, text blocks concatenated", resp.Text)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestCompleteOverloadedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(529)
		fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer srv.Close()

	c := New("sk-test", "test-model", srv.URL)
	_, err := c.Complete(context.Background(), provider.Request{User: "x", MaxTokens: 10})
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %v", err)
	}
	if apiErr.Status != 529 || apiErr.Type != "overloaded_error" || !apiErr.Retryable() || apiErr.RetryAfter != 3*time.Second {
		t.Fatalf("bad error mapping: %+v", apiErr)
	}
}

func TestCompleteTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release) // unblock the handler before srv.Close waits on it

	c := New("sk-test", "test-model", srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, provider.Request{User: "x", MaxTokens: 10})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestCompleteMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{not json`)
	}))
	defer srv.Close()

	c := New("sk-test", "test-model", srv.URL)
	if _, err := c.Complete(context.Background(), provider.Request{User: "x", MaxTokens: 10}); err == nil {
		t.Fatal("want decode error, got nil")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New("k", "m", "")
	if c.BaseURL != DefaultBaseURL {
		t.Fatalf("got %q", c.BaseURL)
	}
	if c.Name() != "anthropic" {
		t.Fatalf("name %q", c.Name())
	}
}
