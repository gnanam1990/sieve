package gh

import (
	"context"
	"errors"
)

// TokenSource resolves a GitHub token at request time.
//
// The client holds a TokenSource, never a bare string. This is the
// daemon-mode guard: GitHub App installation tokens (short-lived, refreshed
// per installation) become a second TokenSource implementation later rather
// than a refactor of every call site. Environment access stays confined to
// cmd/, which resolves a value and wraps it in a StaticTokenSource.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticTokenSource wraps a fixed token resolved once from the flag/env in
// cmd/. An empty token is reported as an error at first use so the fail-fast
// contract survives the move away from a bare string field.
type StaticTokenSource struct {
	tok string
}

// NewStaticTokenSource wraps an already-resolved token string.
func NewStaticTokenSource(tok string) StaticTokenSource {
	return StaticTokenSource{tok: tok}
}

// Token returns the fixed token, or an error when it is empty.
func (s StaticTokenSource) Token(context.Context) (string, error) {
	if s.tok == "" {
		return "", errors.New("no GitHub token: pass --token or set GITHUB_TOKEN")
	}
	return s.tok, nil
}
