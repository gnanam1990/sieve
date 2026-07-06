package post

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// walkInternalSources visits every non-test .go file under internal/ (the test
// runs from internal/post, so ".." is internal/), calling fn(relPath, src).
func walkInternalSources(t *testing.T, fn func(path string, src string)) {
	t.Helper()
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // walking our own source tree in-test
		if err != nil {
			return err
		}
		fn(filepath.ToSlash(path), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGitHubWriteTransportOnlyInPost is the precise R1.2 enforcement: the only
// GitHub write transport is gh.Client.Send, and it must be invoked exclusively
// from this package. Anything else calling .Send would be a GitHub mutation
// escaping internal/post.
func TestGitHubWriteTransportOnlyInPost(t *testing.T) {
	sendCall := regexp.MustCompile(`\.Send\(`)
	var offenders []string
	walkInternalSources(t, func(path, src string) {
		if strings.HasPrefix(path, "../post/") {
			return // this package is allowed to call the write transport
		}
		if strings.HasPrefix(path, "../gh/") {
			return // gh defines Send but never calls it
		}
		if sendCall.MatchString(src) {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("gh write transport (.Send) used outside internal/post: %v", offenders)
	}
}

// TestMutatingMethodsConfinedToPost is the broad-net version: no package under
// internal/ may reference a mutating HTTP method, EXCEPT internal/post (GitHub
// writes) and internal/provider (LLM API requests, which target the model
// endpoint, not the GitHub client, and so are outside R1.2's scope).
func TestMutatingMethodsConfinedToPost(t *testing.T) {
	mutating := regexp.MustCompile(`http\.Method(Post|Patch|Put|Delete)`)
	var offenders []string
	walkInternalSources(t, func(path, src string) {
		switch {
		case strings.HasPrefix(path, "../post/"):
			return
		case strings.HasPrefix(path, "../provider/"):
			return // LLM transport, not GitHub
		case strings.HasSuffix(path, "gh/graphql.go"):
			return // a GraphQL read query (POST to /graphql), not a REST mutation
		}
		if mutating.MatchString(src) {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("mutating HTTP method referenced outside internal/post (and the LLM provider): %v", offenders)
	}
}
