// Package post performs every GitHub write sieve makes: creating or editing
// the single walkthrough comment and submitting the inline review. No other
// package may call a mutating GitHub endpoint — a test greps for it.
//
// Writes are deliberately single-shot (see gh.Send): a review or comment POST
// is not idempotent, so a blind retry could double-post. The dedupe design
// (locate-by-marker, fingerprint cross-run skip) is what makes re-runs safe,
// not transport retries.
package post

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/render"
)

// Poster owns the write endpoints for one PR.
type Poster struct {
	Client *gh.Client
	Owner  string
	Repo   string
	PR     int
	Log    *slog.Logger
}

// Locator is the outcome of finding (or not finding) the existing walkthrough
// comment, plus the metadata decoded from it for cross-run dedupe.
type Locator struct {
	Found     bool
	CommentID int64
	Meta      gate.Meta
	HasMeta   bool
}

// LocateWalkthrough paginates the PR's issue comments and finds the one
// carrying sieve's marker. Multiple matches (an unusual, hand-created state)
// resolve to the first, with a warning. The prior metadata is decoded so the
// gate can detect repeated/resolved findings.
func (p *Poster) LocateWalkthrough(ctx context.Context) (Locator, error) {
	comments, err := p.Client.ListIssueComments(ctx, p.Owner, p.Repo, p.PR)
	if err != nil {
		return Locator{}, err
	}
	var loc Locator
	matches := 0
	for _, c := range comments {
		if !containsMarker(c.Body) {
			continue
		}
		matches++
		if matches > 1 {
			continue
		}
		loc.Found = true
		loc.CommentID = c.ID
		meta, ok, mErr := render.ExtractMeta(c.Body)
		if mErr != nil {
			p.Log.Warn("existing walkthrough has an unreadable metadata block; cross-run dedupe degraded", "err", mErr)
		}
		loc.Meta, loc.HasMeta = meta, ok
	}
	if matches > 1 {
		p.Log.Warn("multiple sieve walkthrough comments found; editing the first", "count", matches)
	}
	return loc, nil
}

func containsMarker(body string) bool {
	return bytes.Contains([]byte(body), []byte(render.WalkthroughMarker))
}

// UpsertWalkthrough edits the located walkthrough in place, or creates one when
// none exists.
func (p *Poster) UpsertWalkthrough(ctx context.Context, loc Locator, body string) error {
	payload := map[string]string{"body": body}
	if loc.Found {
		url := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d", p.Client.BaseURL, p.Owner, p.Repo, loc.CommentID)
		status, respBody, err := p.write(ctx, http.MethodPatch, url, payload)
		if err != nil {
			return fmt.Errorf("edit walkthrough: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("edit walkthrough: %s", ghError(status, respBody))
		}
		p.Log.Info("walkthrough updated", "comment_id", loc.CommentID)
		return nil
	}
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", p.Client.BaseURL, p.Owner, p.Repo, p.PR)
	status, respBody, err := p.write(ctx, http.MethodPost, url, payload)
	if err != nil {
		return fmt.Errorf("create walkthrough: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create walkthrough: %s", ghError(status, respBody))
	}
	p.Log.Info("walkthrough created")
	return nil
}

// write marshals payload as JSON and issues a single mutating request via the
// gh transport. It returns the status and (bounded) response body so callers
// can branch on 422 etc.
func (p *Poster) write(ctx context.Context, method, url string, payload any) (int, []byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Send(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort drain
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, respBody, nil
}

// ghError formats a GitHub error response, surfacing its "message" when present.
func ghError(status int, body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		return fmt.Sprintf("GitHub returned %d: %s", status, parsed.Message)
	}
	return fmt.Sprintf("GitHub returned %d", status)
}
