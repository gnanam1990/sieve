package filter

import (
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/diff"
)

func modified(path string) diff.FileDiff {
	return diff.FileDiff{OldPath: path, NewPath: path, Status: diff.Modified}
}

func TestApplyDefaults(t *testing.T) {
	cases := []struct {
		path       string
		skipped    bool
		reasonPart string
	}{
		{"main.go", false, ""},
		{"go.mod", false, ""}, // dependency changes are reviewable
		{"go.sum", true, "go.sum"},
		{"deep/nested/go.sum", true, "go.sum"},
		{"vendor/github.com/x/y.go", true, "vendor"},
		{"pkg/vendor/z.go", true, "vendor"},
		{"node_modules/left-pad/index.js", true, "node_modules"},
		{"web/dist/app.js", true, "dist"},
		{"assets/app.min.js", true, "min.js"},
		{"styles/site.min.css", true, "min.css"},
		{"out/bundle.js.map", true, "map"},
		{"api/service.pb.go", true, "pb.go"},
		{"models/user_generated.go", true, "generated"},
		{"schema/types.gen.go", true, "gen.go"},
		{"package-lock.json", true, "package-lock"},
		{"frontend/yarn.lock", true, "yarn.lock"},
		{"Cargo.lock", true, "Cargo.lock"},
		{"tests/__snapshots__/app.snap", true, "snap"},
		{"docs/logo.png", true, "png"},
		{"fonts/inter.woff2", true, "woff2"},
		{"release/pkg.tar", true, "tar"},
		{"src/tarball.go", false, ""}, // extension match must not catch substrings
		{"builder/main.go", false, ""},
		{"distance/calc.go", false, ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			res, err := Apply([]diff.FileDiff{modified(c.path)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := res[0]
			if r.Skipped != c.skipped {
				t.Fatalf("skipped=%v (reason %q), want %v", r.Skipped, r.SkipReason, c.skipped)
			}
			if c.skipped && !strings.Contains(r.SkipReason, c.reasonPart) {
				t.Fatalf("reason %q should mention %q", r.SkipReason, c.reasonPart)
			}
		})
	}
}

func TestApplyBinaryBeatsEverything(t *testing.T) {
	f := diff.FileDiff{OldPath: "img.bin", NewPath: "img.bin", Status: diff.Binary}
	res, err := Apply([]diff.FileDiff{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Skipped || res[0].SkipReason != "binary file" {
		t.Fatalf("binary file not skipped as binary: %+v", res[0])
	}
}

func TestApplyConfigGlobs(t *testing.T) {
	files := []diff.FileDiff{modified("docs/guide.md"), modified("internal/x.gen.go"), modified("main.go")}
	res, err := Apply(files, []string{"docs/**"})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Skipped || !strings.HasPrefix(res[0].SkipReason, "config exclude:") {
		t.Fatalf("docs/guide.md: %+v", res[0])
	}
	// default excludes win before config globs are consulted
	if !res[1].Skipped || !strings.HasPrefix(res[1].SkipReason, "default exclude:") {
		t.Fatalf("x.gen.go: %+v", res[1])
	}
	if res[2].Skipped {
		t.Fatalf("main.go should be kept: %+v", res[2])
	}
}

func TestApplyDeleteMatchesOldPath(t *testing.T) {
	f := diff.FileDiff{OldPath: "docs/old.md", NewPath: "", Status: diff.Deleted}
	res, err := Apply([]diff.FileDiff{f}, []string{"docs/**"})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Skipped {
		t.Fatalf("deleted file should match config glob via OldPath: %+v", res[0])
	}
}

func TestApplyInvalidGlob(t *testing.T) {
	if _, err := Apply([]diff.FileDiff{modified("a.go")}, []string{"[bad"}); err == nil {
		t.Fatal("want error for invalid glob, got nil")
	}
}
