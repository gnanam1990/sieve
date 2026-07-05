// Package anthropic adapts the Anthropic Messages API to the provider
// interface. Single-shot, non-streaming.
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gnanam1990/sieve/internal/provider"
)

// DefaultBaseURL is the production Anthropic API host.
const DefaultBaseURL = "https://api.anthropic.com"

// Client calls POST {base}/v1/messages.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// New returns an Anthropic client for the given model.
func New(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 10 * time.Minute}, // per-request deadline comes from ctx
	}
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "anthropic" }

type messagesRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	System      string    `json:"system,omitempty"`
	Messages    []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements provider.Provider.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	payload := messagesRequest{
		Model:       c.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		System:      req.System,
		Messages:    []message{{Role: "user", Content: req.User}},
	}
	headers := map[string]string{
		"x-api-key":         c.APIKey,
		"anthropic-version": "2023-06-01",
	}
	var out messagesResponse
	if err := provider.PostJSON(ctx, c.HTTP, c.BaseURL+"/v1/messages", headers, payload, &out); err != nil {
		return provider.Response{}, fmt.Errorf("anthropic: %w", err)
	}
	var sb strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return provider.Response{
		Text: sb.String(),
		Usage: provider.Usage{
			InputTokens:  out.Usage.InputTokens,
			OutputTokens: out.Usage.OutputTokens,
		},
	}, nil
}
