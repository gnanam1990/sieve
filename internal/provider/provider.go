// Package provider defines the single-shot LLM completion abstraction and
// the shared retry decorator.
//
// Deliberately minimal: no streaming, no tool calling, no conversation
// state. One request in, one response out.
package provider

import "context"

// Request is a single completion request.
type Request struct {
	System      string
	User        string
	MaxTokens   int
	Temperature float64
}

// Usage is the provider-reported token accounting for one call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is the completed text plus usage.
type Response struct {
	Text  string
	Usage Usage
}

// Provider is a blocking single-shot completion backend.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Name() string // "anthropic", "openai-compat", "fake"
}
