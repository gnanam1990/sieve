package post

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/render"
)

// InlineComment is one entry of a review's comments array. For a single-line
// finding only Line/Side are set; a range also sets StartLine/StartSide, and
// GitHub's Line is the range's *last* line.
type InlineComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// BuildInlineComments turns the inline tier into GitHub review comments,
// skipping findings marked Repeated (their thread already exists) and using
// render for the bodies. anchors decides suggestion committability.
func BuildInlineComments(inline []gate.Finding, anchors *findings.Anchors) []InlineComment {
	var out []InlineComment
	for _, f := range inline {
		if f.Repeated {
			continue
		}
		c := InlineComment{
			Path: f.Path,
			Line: f.Line,
			Side: string(f.Side),
			Body: render.Inline(f, anchors),
		}
		if f.EndLine > 0 {
			// GitHub multi-line comments: Line is the end, StartLine the start.
			c.StartLine = f.Line
			c.StartSide = string(f.Side)
			c.Line = f.EndLine
		}
		out = append(out, c)
	}
	return out
}

// CollectCids lists the PR's inline review comments and maps sieve's own
// comments (those carrying a fingerprint marker) from fingerprint to comment
// ID. This recovers cids for the walkthrough metadata without server-side
// state, and is the basis of sync and reaction fetching.
func (p *Poster) CollectCids(ctx context.Context) (map[string]int64, error) {
	comments, err := p.Client.ListReviewComments(ctx, p.Owner, p.Repo, p.PR)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64)
	for _, c := range comments {
		if fp := render.ParseFpMarker(c.Body); fp != "" {
			out[fp] = c.ID
		}
	}
	return out, nil
}

// reviewPayload is the single-submission review body.
type reviewPayload struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"`
	Comments []InlineComment `json:"comments"`
}

// individualPayload is one comment posted via the review-comments endpoint in
// the 422 fallback path. It carries commit_id per comment.
type individualPayload struct {
	CommitID  string `json:"commit_id"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// SubmitInlineReview posts every comment in a single review (event COMMENT) —
// one notification, one atomic batch. On a 422 for the batch it falls back to
// posting each comment individually, skipping and counting the ones that still
// fail. The returned count is InlinePostFailed; any value > 0 maps to exit
// code 2. A hard transport/HTTP failure of the batch (not a 422) is reported
// as every comment failing, with the error for logging.
func (p *Poster) SubmitInlineReview(ctx context.Context, headSHA string, comments []InlineComment) (failed int, err error) {
	if len(comments) == 0 {
		return 0, nil
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", p.Client.BaseURL, p.Owner, p.Repo, p.PR)
	status, body, err := p.write(ctx, http.MethodPost, url, reviewPayload{
		CommitID: headSHA,
		Event:    "COMMENT",
		Comments: comments,
	})
	if err != nil {
		return len(comments), fmt.Errorf("submit review: %w", err)
	}
	if status == http.StatusOK || status == http.StatusCreated {
		p.Log.Info("inline review submitted", "comments", len(comments))
		return 0, nil
	}
	if status == http.StatusUnprocessableEntity {
		p.Log.Warn("batch review rejected (422); falling back to individual comments", "detail", ghError(status, body))
		return p.postIndividually(ctx, headSHA, comments)
	}
	// Any other non-2xx: the whole batch failed and we cannot recover it.
	return len(comments), fmt.Errorf("submit review: %s", ghError(status, body))
}

// postIndividually is the 422 fallback: one POST per comment to the
// review-comments endpoint, counting failures without aborting the rest.
func (p *Poster) postIndividually(ctx context.Context, headSHA string, comments []InlineComment) (int, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", p.Client.BaseURL, p.Owner, p.Repo, p.PR)
	failed := 0
	for _, c := range comments {
		status, body, err := p.write(ctx, http.MethodPost, url, individualPayload{
			CommitID:  headSHA,
			Path:      c.Path,
			Line:      c.Line,
			Side:      c.Side,
			StartLine: c.StartLine,
			StartSide: c.StartSide,
			Body:      c.Body,
		})
		switch {
		case err != nil:
			failed++
			p.Log.Error("inline comment failed to post", "path", c.Path, "line", c.Line, "err", err)
		case status != http.StatusCreated && status != http.StatusOK:
			failed++
			p.Log.Error("inline comment rejected", "path", c.Path, "line", c.Line, "detail", ghError(status, body))
		default:
			p.Log.Debug("inline comment posted individually", "path", c.Path, "line", c.Line)
		}
	}
	if failed > 0 {
		p.Log.Warn("some inline comments could not be posted", "failed", failed, "total", len(comments))
	}
	return failed, nil
}
