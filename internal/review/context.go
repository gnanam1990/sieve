package review

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gnanam1990/sieve/internal/blast"
	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/grammar"
	"github.com/gnanam1990/sieve/internal/prompt"
	"github.com/gnanam1990/sieve/internal/repomap"
	"github.com/gnanam1990/sieve/internal/symbols"
)

// buildExtraContext renders the Stage 8 repository-context section for the
// prompt. It uses cfg.Review.ContextDepth to decide how much surrounding
// context to attach.
func buildExtraContext(ctx context.Context, cfg config.Config, repoPath string, files []prompt.File) (string, error) {
	switch cfg.Review.ContextDepth {
	case "symbols":
		return renderSymbolsContext(ctx, symbols.Default(), files)
	case "repomap":
		return renderRepoMapContext(ctx, cfg, repoPath)
	case "blast":
		return renderBlastContext(ctx, cfg, repoPath, files)
	default:
		return "", nil
	}
}

func renderSymbolsContext(ctx context.Context, registry symbols.Registry, files []prompt.File) (string, error) {
	var b strings.Builder
	hasAny := false
	for _, f := range files {
		if len(f.Content) == 0 {
			continue
		}
		syms, err := registry.Extract(ctx, f.Path, f.Content)
		if err != nil {
			continue
		}
		if len(syms) == 0 {
			continue
		}
		hasAny = true
		fmt.Fprintf(&b, "- %s: ", f.Path)
		for i, s := range syms {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(symbolSummary(s))
		}
		b.WriteString("\n")
	}
	if !hasAny {
		return "", nil
	}
	return "## Symbols in changed files\n\n" + b.String(), nil
}

func symbolSummary(s symbols.Symbol) string {
	if s.Signature != "" {
		return fmt.Sprintf("%s %s %s", s.Kind, s.Name, s.Signature)
	}
	return fmt.Sprintf("%s %s", s.Kind, s.Name)
}

func renderRepoMapContext(ctx context.Context, cfg config.Config, repoPath string) (string, error) {
	if repoPath == "" {
		return "", nil
	}
	rt, err := grammar.New(ctx)
	if err != nil {
		return "", err
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)

	m, err := repomap.Build(ctx, repomap.Options{
		Root:       repoPath,
		Registry:   symbols.DefaultWithGrammar(pool),
		MaxFiles:   cfg.Review.ContextMaxFiles,
		MaxTokens:  cfg.Review.ContextMaxTokens,
		Languages:  cfg.Review.ContextLangs,
	})
	if err != nil {
		return "", err
	}
	if len(m.Entries) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("## Repo map\n\n")
	for _, e := range m.Entries {
		fmt.Fprintf(&b, "- %s (%s): %d symbols, %d imports\n", e.Path, e.Language, len(e.Symbols), len(e.Imports))
	}
	return b.String(), nil
}

func renderBlastContext(ctx context.Context, cfg config.Config, repoPath string, files []prompt.File) (string, error) {
	if repoPath == "" {
		return "", nil
	}
	rt, err := grammar.New(ctx)
	if err != nil {
		return "", err
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)

	rm, err := repomap.Build(ctx, repomap.Options{
		Root:       repoPath,
		Registry:   symbols.DefaultWithGrammar(pool),
		MaxFiles:   cfg.Review.ContextMaxFiles,
		MaxTokens:  cfg.Review.ContextMaxTokens,
		Languages:  cfg.Review.ContextLangs,
	})
	if err != nil {
		return "", err
	}

	var changed []string
	for _, f := range files {
		changed = append(changed, f.Path)
	}
	sort.Strings(changed)

	r := blast.Compute(rm, changed)
	if len(r.Direct) == 0 && len(r.Indirect) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("## Blast radius\n\n")
	if len(r.Direct) > 0 {
		b.WriteString("Directly affected files:\n")
		for _, p := range r.Direct {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if len(r.Indirect) > 0 {
		if len(r.Direct) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Indirectly affected files:\n")
		for _, p := range r.Indirect {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String(), nil
}
