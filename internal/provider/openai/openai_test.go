package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnanam1990/sieve/internal/provider"
)

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("bad auth: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
			t.Errorf("bad messages: %+v", body.Messages)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"answer"}}],
			"usage":{"prompt_tokens":21,"completion_tokens":9}}`)
	}))
	defer srv.Close()

	c := New("sk-test", "test-model", srv.URL+"/v1/")
	resp, err := c.Complete(context.Background(), provider.Request{System: "sys", User: "q", MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "answer" || resp.Usage.InputTokens != 21 || resp.Usage.OutputTokens != 9 {
		t.Fatalf("resp %+v", resp)
	}
}

func TestCompleteNoSystemMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
			t.Errorf("empty system must be omitted: %+v", body.Messages)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := New("k", "m", srv.URL)
	resp, err := c.Complete(context.Background(), provider.Request{User: "q", MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	// Missing usage (some compat servers omit it) must not be an error.
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestCompleteRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer srv.Close()

	c := New("k", "m", srv.URL)
	_, err := c.Complete(context.Background(), provider.Request{User: "q", MaxTokens: 10})
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || !apiErr.Retryable() {
		t.Fatalf("want retryable 429 APIError, got %v", err)
	}
}

func TestCompleteEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	c := New("k", "m", srv.URL)
	if _, err := c.Complete(context.Background(), provider.Request{User: "q", MaxTokens: 10}); err == nil {
		t.Fatal("want error for empty choices")
	}
}

func TestName(t *testing.T) {
	if New("k", "m", "http://x").Name() != "openai-compat" {
		t.Fatal("bad name")
	}
}
