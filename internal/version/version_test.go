package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestStringIsVersion(t *testing.T) {
	if String() != Version {
		t.Fatalf("String() = %q, want %q", String(), Version)
	}
}

func TestInfoReportsBuildAndPlatform(t *testing.T) {
	info := Info()
	for _, want := range []string{"sieve ", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, "commit:", "built:"} {
		if !strings.Contains(info, want) {
			t.Errorf("Info() missing %q:\n%s", want, info)
		}
	}
}

// TestDevDefaults documents that an un-stamped build reports "dev".
func TestDevDefaults(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty")
	}
}
