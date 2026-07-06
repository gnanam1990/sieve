package fake

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/sieve/internal/provider"
)

func TestComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canned.json")
	canned := `{"findings":[]}`
	if err := os.WriteFile(path, []byte(canned), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	if c.Name() != "fake" {
		t.Fatalf("name %q", c.Name())
	}
	resp, err := c.Complete(context.Background(), provider.Request{System: "abcd", User: "efgh"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != canned {
		t.Fatalf("text %q", resp.Text)
	}
	if resp.Usage.InputTokens != 2 || resp.Usage.OutputTokens != len(canned)/4 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestCompleteMissingFixture(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nope.json"))
	if _, err := c.Complete(context.Background(), provider.Request{}); err == nil {
		t.Fatal("want error for missing fixture")
	}
}
