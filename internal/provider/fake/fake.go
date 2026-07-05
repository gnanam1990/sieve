// Package fake is a deterministic provider for tests and offline E2E runs.
// It returns the contents of a fixture file as the completion text.
// Not for real use.
package fake

import (
	"context"
	"fmt"
	"os"

	"github.com/gnanam1990/sieve/internal/provider"
)

// Client returns a canned response from a fixture file.
type Client struct {
	FixturePath string
}

// New returns a fake provider backed by the fixture at path.
func New(fixturePath string) *Client {
	return &Client{FixturePath: fixturePath}
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "fake" }

// Complete implements provider.Provider. Usage is estimated at bytes/4 so
// stats plumbing is exercised deterministically.
func (c *Client) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	data, err := os.ReadFile(c.FixturePath)
	if err != nil {
		return provider.Response{}, fmt.Errorf("fake provider: %w", err)
	}
	return provider.Response{
		Text: string(data),
		Usage: provider.Usage{
			InputTokens:  (len(req.System) + len(req.User)) / 4,
			OutputTokens: len(data) / 4,
		},
	}, nil
}
