// Package gh is a minimal read-only GitHub REST v3 client for the handful
// of endpoints sieve needs. Deliberately not go-github: ~6 endpoints do not
// justify the dependency.
package gh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// MaxDiffBytes is the hard cap on a fetched diff. Beyond it the diff is
// truncated at the last complete file boundary.
const MaxDiffBytes = 5 << 20

const maxAttempts = 4

// Client talks to the GitHub REST API using only stdlib net/http.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Log     *slog.Logger

	// RetryBase is the first backoff delay; tests shrink it.
	RetryBase time.Duration
}

// New returns a Client for api.github.com. A missing token fails fast here
// so every fetch path gives the same clear error.
func New(token string, logger *slog.Logger) (*Client, error) {
	if token == "" {
		return nil, errors.New("no GitHub token: pass --token or set GITHUB_TOKEN")
	}
	return &Client{
		BaseURL:   "https://api.github.com",
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Log:       logger,
		RetryBase: 500 * time.Millisecond,
	}, nil
}

// PullRequest holds only the PR fields sieve uses.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

// PRFile is one row of the pulls/{n}/files listing.
type PRFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

// GetPR fetches PR metadata.
func (c *Client) GetPR(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	var pr PullRequest
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.BaseURL, owner, repo, number)
	if err := c.getJSON(ctx, url, "application/vnd.github+json", &pr); err != nil {
		return nil, fmt.Errorf("get PR %s/%s#%d: %w", owner, repo, number, err)
	}
	return &pr, nil
}

// GetDiff fetches the raw unified diff. Diffs beyond MaxDiffBytes are cut
// at the last complete file boundary and reported truncated.
func (c *Client) GetDiff(ctx context.Context, owner, repo string, number int) (data []byte, truncated bool, err error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.BaseURL, owner, repo, number)
	resp, err := c.do(ctx, url, "application/vnd.github.v3.diff")
	if err != nil {
		return nil, false, fmt.Errorf("get diff %s/%s#%d: %w", owner, repo, number, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body

	buf, err := io.ReadAll(io.LimitReader(resp.Body, MaxDiffBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read diff: %w", err)
	}
	if len(buf) <= MaxDiffBytes {
		return buf, false, nil
	}
	cut := buf[:MaxDiffBytes]
	if i := lastFileBoundary(cut); i > 0 {
		cut = cut[:i]
	}
	c.Log.Warn("diff exceeds cap, truncated at last complete file boundary",
		"cap_bytes", MaxDiffBytes, "kept_bytes", len(cut))
	return cut, true, nil
}

// lastFileBoundary returns the offset of the last "diff --git" header in
// buf, i.e. the start of the (possibly incomplete) final file entry.
func lastFileBoundary(buf []byte) int {
	i := strings.LastIndex(string(buf), "\ndiff --git ")
	if i < 0 {
		return 0
	}
	return i + 1
}

// FilesCap is GitHub's hard limit on the files listing.
const FilesCap = 3000

// ListFiles pages through pulls/{n}/files at 100/page. If GitHub's
// 3000-file cap is hit, truncated is true.
func (c *Client) ListFiles(ctx context.Context, owner, repo string, number int) (files []PRFile, truncated bool, err error) {
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", c.BaseURL, owner, repo, number, page)
		var batch []PRFile
		if err := c.getJSON(ctx, url, "application/vnd.github+json", &batch); err != nil {
			return nil, false, fmt.Errorf("list files %s/%s#%d page %d: %w", owner, repo, number, page, err)
		}
		files = append(files, batch...)
		if len(files) >= FilesCap {
			c.Log.Warn("PR file listing hit GitHub's 3000-file cap; listing is incomplete", "files", len(files))
			return files, true, nil
		}
		if len(batch) < 100 {
			return files, false, nil
		}
	}
}

// GetContents fetches a file's content at a ref and base64-decodes it.
// (Built now, consumed in stage 2.)
func (c *Client) GetContents(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	var out struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", c.BaseURL, owner, repo, path, ref)
	if err := c.getJSON(ctx, url, "application/vnd.github+json", &out); err != nil {
		return nil, fmt.Errorf("get contents %s@%s: %w", path, ref, err)
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("get contents %s@%s: unexpected encoding %q", path, ref, out.Encoding)
	}
	// GitHub wraps base64 payloads in newlines.
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("get contents %s@%s: decode: %w", path, ref, err)
	}
	return data, nil
}

func (c *Client) getJSON(ctx context.Context, url, accept string, v any) error {
	resp, err := c.do(ctx, url, accept)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// do issues a GET with auth and retries: exponential backoff + jitter,
// honoring Retry-After on secondary rate limits, retrying 429 and 5xx.
func (c *Client) do(ctx context.Context, url, accept string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.backoff(attempt, lastErr)); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		c.Log.Debug("github request", "url", url, "status", resp.StatusCode,
			"ratelimit_remaining", resp.Header.Get("X-RateLimit-Remaining"))

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close() //nolint:errcheck,gosec // best-effort drain on error path
		lastErr = &apiError{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After"), body: string(body)}
		if !retryable(resp) {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

type apiError struct {
	status     int
	retryAfter string
	body       string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("GitHub API returned %d", e.status)
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.body), &parsed) == nil && parsed.Message != "" {
		msg += ": " + parsed.Message
	}
	return msg
}

// retryable: 429, 5xx, and 403 secondary rate limits (signalled by
// Retry-After or an exhausted primary quota).
func retryable(resp *http.Response) bool {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode >= 500:
		return true
	case resp.StatusCode == http.StatusForbidden:
		return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"
	default:
		return false
	}
}

func (c *Client) backoff(attempt int, lastErr error) time.Duration {
	var ae *apiError
	if errors.As(lastErr, &ae) && ae.retryAfter != "" {
		if secs, err := strconv.Atoi(ae.retryAfter); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	base := c.RetryBase * time.Duration(1<<(attempt-1))
	jitter := time.Duration(rand.Int64N(int64(c.RetryBase))) //nolint:gosec // jitter, not crypto
	return base + jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RepoFromEnv returns owner/name from GITHUB_REPOSITORY (Actions mode).
func RepoFromEnv() string {
	return os.Getenv("GITHUB_REPOSITORY")
}

// PRNumberFromEnv reads the Actions event payload at GITHUB_EVENT_PATH and
// returns pull_request.number, or 0 if unavailable.
func PRNumberFromEnv() int {
	path := os.Getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path) //nolint:gosec // GITHUB_EVENT_PATH is the Actions runtime contract
	if err != nil {
		return 0
	}
	var event struct {
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &event) != nil {
		return 0
	}
	return event.PullRequest.Number
}
