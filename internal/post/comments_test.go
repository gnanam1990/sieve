package post

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gnanam1990/sieve/internal/render"
)

func TestCommentsAndCids(t *testing.T) {
	fp := "0123456789abcdef"
	body := "**[major] T**\n\nwhy\n\n<sub>sieve · category `bug` · confidence 0.90</sub>\n" +
		render.FpMarkerPrefix + "v1 " + fp + render.FpMarkerSuffix + "\n"
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/7/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprintf(w, `[{"id":555,"path":"a.go","body":%q,"reactions":{"+1":2,"-1":1}},{"id":556,"body":"a human comment"}]`, body)
	}))
	comments, err := p.Comments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].Reactions.PlusOne != 2 || comments[0].Reactions.MinusOne != 1 {
		t.Fatalf("comments/reactions wrong: %+v", comments)
	}
	cids := CidsOf(comments)
	if len(cids) != 1 || cids[fp] != 555 {
		t.Fatalf("CidsOf wrong: %+v", cids)
	}
	// CollectCids goes through the same path.
	got, err := p.CollectCids(context.Background())
	if err != nil || got[fp] != 555 {
		t.Fatalf("CollectCids: %+v err %v", got, err)
	}
}

func TestResolvedThreadsPoster(t *testing.T) {
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":true,"comments":{"nodes":[{"databaseId":9,"body":"x"}]}}]}}}}}`)
	}))
	threads, err := p.ResolvedThreads(context.Background())
	if err != nil || len(threads) != 1 || !threads[0].IsResolved {
		t.Fatalf("threads=%+v err=%v", threads, err)
	}
}
