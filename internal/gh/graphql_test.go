package gh

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResolvedThreads(t *testing.T) {
	var gotQuery string
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("expected POST /graphql, got %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotQuery = string(body)
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"isResolved":true,"comments":{"nodes":[{"databaseId":111,"body":"resolved one"}]}},
			{"isResolved":false,"comments":{"nodes":[{"databaseId":222,"body":"open one"}]}}
		]}}}}}`)
	}))
	threads, err := c.ResolvedThreads(context.Background(), "acme", "app", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "reviewThreads") || !strings.Contains(gotQuery, `"pr":7`) {
		t.Fatalf("query/variables wrong: %s", gotQuery)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads", len(threads))
	}
	if !threads[0].IsResolved || threads[0].Comments[0].DatabaseID != 111 {
		t.Fatalf("resolved thread wrong: %+v", threads[0])
	}
	if threads[1].IsResolved {
		t.Fatalf("second thread should be open: %+v", threads[1])
	}
}

func TestResolvedThreadsGraphQLError(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"errors":[{"message":"Could not resolve to a Repository"}]}`)
	}))
	if _, err := c.ResolvedThreads(context.Background(), "o", "r", 1); err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("graphql error must surface: %v", err)
	}
}
