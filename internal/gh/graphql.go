package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ReviewThread is one PR review thread with its resolution state and comments.
// sieve reads these (dismissal detection) because the REST API cannot report
// whether a review thread has been resolved — only GraphQL exposes it.
type ReviewThread struct {
	IsResolved bool
	Comments   []ThreadComment
}

// ThreadComment carries the REST comment id (databaseId) and body so sieve can
// match its own comments by their fingerprint marker.
type ThreadComment struct {
	DatabaseID int64
	Body       string
}

// reviewThreadsQuery is the single pinned GraphQL query sieve issues. It is a
// read; the POST goes to /graphql, never a REST mutation endpoint.
const reviewThreadsQuery = `query($owner:String!,$name:String!,$pr:Int!){repository(owner:$owner,name:$name){pullRequest(number:$pr){reviewThreads(first:100){nodes{isResolved comments(first:20){nodes{databaseId body}}}}}}}`

// ResolvedThreads runs the pinned query and returns the PR's review threads.
// Capped at the first 100 threads (a warning is logged if that cap is hit).
func (c *Client) ResolvedThreads(ctx context.Context, owner, repo string, number int) ([]ReviewThread, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     reviewThreadsQuery,
		"variables": map[string]any{"owner": owner, "name": repo, "pr": number},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Send(req) // applies auth; single-shot is fine for a query
	if err != nil {
		return nil, fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql returned %d", resp.StatusCode)
	}

	var out struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64  `json:"databaseId"`
									Body       string `json:"body"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("graphql decode: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", out.Errors[0].Message)
	}

	nodes := out.Data.Repository.PullRequest.ReviewThreads.Nodes
	if len(nodes) == 100 {
		c.Log.Warn("review threads hit the 100-thread query cap; dismissal detection may be incomplete")
	}
	threads := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		th := ReviewThread{IsResolved: n.IsResolved}
		for _, cm := range n.Comments.Nodes {
			th.Comments = append(th.Comments, ThreadComment{DatabaseID: cm.DatabaseID, Body: cm.Body})
		}
		threads = append(threads, th)
	}
	return threads, nil
}
