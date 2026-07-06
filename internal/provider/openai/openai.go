// Package openai adapts any OpenAI-compatible chat-completions endpoint
// (OpenAI, OpenRouter, Groq, local Ollama) to the provider interface.
// Single-shot, non-streaming. base_url is required — there is no default,
// which forces the operator to be explicit about where requests go.
package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gnanam1990/sieve/internal/provider"
)

// Client calls POST {base_url}/chat/completions.
type Client struct {
	BaseURL string // e.g. https://api.openai.com/v1, http://localhost:11434/v1
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// New returns an OpenAI-compatible client for the given model.
func New(apiKey, model, baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 10 * time.Minute}, // per-request deadline comes from ctx
	}
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "openai-compat" }

type chatRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	Messages    []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete implements provider.Provider.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	msgs := make([]message, 0, 2)
	if req.System != "" {
		msgs = append(msgs, message{Role: "system", Content: req.System})
	}
	msgs = append(msgs, message{Role: "user", Content: req.User})
	payload := chatRequest{
		Model:       c.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Messages:    msgs,
	}
	headers := map[string]string{"Authorization": "Bearer " + c.APIKey}
	var out chatResponse
	if err := provider.PostJSON(ctx, c.HTTP, c.BaseURL+"/chat/completions", headers, payload, &out); err != nil {
		return provider.Response{}, fmt.Errorf("openai-compat: %w", err)
	}
	if len(out.Choices) == 0 {
		return provider.Response{}, fmt.Errorf("openai-compat: response has no choices")
	}
	// Some compat servers omit usage entirely; zero usage is not an error.
	return provider.Response{
		Text: out.Choices[0].Message.Content,
		Usage: provider.Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
		},
	}, nil
}
