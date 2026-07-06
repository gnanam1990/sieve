package learnings

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gnanam1990/sieve/internal/provider"
)

// draftSystem is the golden-pinned system prompt for rule drafting.
const draftSystem = `You write repository-specific review rules for a code-review tool.
A cluster of findings was repeatedly rejected by maintainers (thumbs-down or dismissed).
Draft ONE rule that will SUPPRESS that class of finding in future reviews.

Requirements:
- Suppressive only: it tells the reviewer what NOT to flag, never what to add.
- Imperative, one sentence, at most 200 characters.
- Generalized: no line numbers, no PR or issue references, no file paths, no verbatim code.
- Specific enough to be actionable for this category and area.

Respond with a single JSON object and nothing else: {"rule": "<the rule>"}`

const correctiveNote = "\n\nYour previous reply was not the required JSON. Resend ONLY {\"rule\": \"<one imperative suppressive sentence, <=200 chars>\"}."

// DraftPrompt builds the user prompt for a cluster (exported for the golden test).
func DraftPrompt(c Cluster) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Category: %s\nArea: %s\nRejected findings (titles):\n", c.Category, c.PathPrefix)
	for _, t := range c.Titles {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	b.WriteString("\nDraft one suppressive rule generalizing why this whole class should not be flagged here.")
	return b.String()
}

// DraftRule asks the provider for one rule for the cluster, with a single
// corrective retry (the stage-2 mechanism) if the reply is not strict JSON, and
// validates the result. A rule that cannot be drafted or fails validation
// returns an error and is skipped by the caller.
func DraftRule(ctx context.Context, p provider.Provider, maxTokens int, temperature float64, c Cluster) (Rule, error) {
	req := provider.Request{System: draftSystem, User: DraftPrompt(c), MaxTokens: maxTokens, Temperature: temperature}
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return Rule{}, err
	}
	rule, perr := parseRule(resp.Text)
	if perr != nil {
		req.User = DraftPrompt(c) + correctiveNote
		resp, err = p.Complete(ctx, req)
		if err != nil {
			return Rule{}, err
		}
		if rule, perr = parseRule(resp.Text); perr != nil {
			return Rule{}, fmt.Errorf("rule not valid JSON after corrective retry: %w", perr)
		}
	}
	if err := validateRule(rule); err != nil {
		return Rule{}, err
	}
	return Rule{Text: rule, Signals: c.Signals()}, nil
}

// parseRule extracts {"rule": "..."} from a model reply, tolerating fences/prose.
func parseRule(text string) (string, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("no JSON object in reply")
	}
	var out struct {
		Rule string `json:"rule"`
	}
	dec := json.NewDecoder(strings.NewReader(text[start : end+1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Rule), nil
}

var (
	lineRefRe = regexp.MustCompile(`(?i)\bline\s+\d+|:\d+\b|#\d+`)
)

// validateRule enforces the rule contract: non-empty, <=200 chars, one
// sentence, no line/PR references, no verbatim code (backticks).
func validateRule(rule string) error {
	if rule == "" {
		return fmt.Errorf("empty rule")
	}
	if len(rule) > MaxRuleLen {
		return fmt.Errorf("rule exceeds %d chars", MaxRuleLen)
	}
	if strings.Contains(rule, "`") {
		return fmt.Errorf("rule contains verbatim code")
	}
	if lineRefRe.MatchString(rule) {
		return fmt.Errorf("rule contains a line/PR reference")
	}
	// One sentence: at most one terminal period (allow a trailing one).
	if strings.Count(strings.TrimRight(rule, "."), ".") > 0 && strings.Contains(strings.TrimRight(rule, "."), ". ") {
		return fmt.Errorf("rule is more than one sentence")
	}
	return nil
}
