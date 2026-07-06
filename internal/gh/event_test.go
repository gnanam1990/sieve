package gh

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEvent(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEventSameRepoNotFork(t *testing.T) {
	path := writeEvent(t, `{
		"pull_request": {"number": 12, "head": {"repo": {"full_name": "acme/app"}}, "base": {"repo": {"full_name": "acme/app"}}},
		"repository": {"full_name": "acme/app"}
	}`)
	ev := EventFromPath(path)
	if !ev.Found || ev.Number != 12 {
		t.Fatalf("event not parsed: %+v", ev)
	}
	if ev.IsFork() {
		t.Fatal("same-repo PR must not be a fork")
	}
}

func TestEventForkIsFork(t *testing.T) {
	path := writeEvent(t, `{
		"pull_request": {"number": 7, "head": {"repo": {"full_name": "forker/app"}}, "base": {"repo": {"full_name": "acme/app"}}},
		"repository": {"full_name": "acme/app"}
	}`)
	ev := EventFromPath(path)
	if !ev.IsFork() || ev.HeadRepo != "forker/app" || ev.BaseRepo != "acme/app" {
		t.Fatalf("fork PR not detected: %+v", ev)
	}
}

func TestEventDeletedHeadIsFork(t *testing.T) {
	// A deleted fork leaves head.repo null; secrets are equally unavailable.
	path := writeEvent(t, `{
		"pull_request": {"number": 9, "head": {"repo": null}, "base": {"repo": {"full_name": "acme/app"}}},
		"repository": {"full_name": "acme/app"}
	}`)
	ev := EventFromPath(path)
	if !ev.IsFork() {
		t.Fatalf("deleted head repo must be treated as a fork: %+v", ev)
	}
}

func TestEventBaseFallsBackToPullRequestBase(t *testing.T) {
	// No top-level repository -> base comes from pull_request.base.repo.
	path := writeEvent(t, `{
		"pull_request": {"number": 3, "head": {"repo": {"full_name": "forker/app"}}, "base": {"repo": {"full_name": "acme/app"}}}
	}`)
	ev := EventFromPath(path)
	if ev.BaseRepo != "acme/app" || !ev.IsFork() {
		t.Fatalf("base fallback wrong: %+v", ev)
	}
}

func TestEventAbsentIsNotFound(t *testing.T) {
	if ev := EventFromPath(""); ev.Found || ev.IsFork() {
		t.Fatalf("empty path must be a non-event: %+v", ev)
	}
	if ev := EventFromPath(filepath.Join(t.TempDir(), "nope.json")); ev.Found {
		t.Fatal("missing file must be a non-event")
	}
}

func TestEventMalformedIsNotFound(t *testing.T) {
	if ev := EventFromPath(writeEvent(t, `{not json`)); ev.Found {
		t.Fatal("malformed event must be a non-event")
	}
}

// TestNonPullRequestEventsAreNotForks guards against skipping legitimate
// same-repo runs: a push / workflow_dispatch / schedule payload carries a
// repository block but no pull_request, and must never be treated as a fork.
func TestNonPullRequestEventsAreNotForks(t *testing.T) {
	cases := map[string]string{
		"push":              `{"ref":"refs/heads/main","repository":{"full_name":"acme/app"}}`,
		"workflow_dispatch": `{"inputs":{},"repository":{"full_name":"acme/app"}}`,
		"schedule":          `{"schedule":"0 0 * * *","repository":{"full_name":"acme/app"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ev := EventFromPath(writeEvent(t, body))
			if ev.Found {
				t.Fatalf("%s event must not be Found (no pull_request): %+v", name, ev)
			}
			if ev.IsFork() {
				t.Fatalf("%s event must never be a fork", name)
			}
		})
	}
}
