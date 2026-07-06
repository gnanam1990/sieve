package memory

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func statsFixture() ([]CategoryStat, Totals) {
	stats := []CategoryStat{
		{Category: "bug", Posted: 12, AddressedByAnchor: 8, PlusOne: 3, MinusOne: 1, Dismissed: 0, MeanConfAddressed: 0.88, MeanConfIgnored: 0.72},
		{Category: "style", Posted: 5, AddressedByAnchor: 1, PlusOne: 0, MinusOne: 4, Dismissed: 2, MeanConfAddressed: 0.65, MeanConfIgnored: 0.70},
	}
	return stats, Totals{Runs: 7, InTok: 18234, OutTok: 1420}
}

func TestWriteStatsGolden(t *testing.T) {
	stats, totals := statsFixture()
	var buf bytes.Buffer
	WriteStats(&buf, stats, totals)
	golden(t, "testdata/stats.golden.txt", buf.String())
	// bug (n>=10) shows a %; style (n<10) shows "-".
	if !strings.Contains(buf.String(), "67%") { // 8/12
		t.Errorf("bug addressed rate missing:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "1 (-)") {
		t.Errorf("style (n<10) must hide the rate:\n%s", buf.String())
	}
}

func TestWriteStatsJSON(t *testing.T) {
	stats, totals := statsFixture()
	var buf bytes.Buffer
	if err := WriteStatsJSON(&buf, stats, totals); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"Category": "bug"`) || !strings.Contains(buf.String(), `"Runs": 7`) {
		t.Fatalf("json shape wrong:\n%s", buf.String())
	}
}

func TestSum(t *testing.T) {
	got := Sum([]Event{
		{Type: TypeRun, InTok: 100, OutTok: 20},
		{Type: TypeRun, InTok: 50, OutTok: 10},
		{Type: TypeFinding, Fp: "x"},
	})
	if got.Runs != 2 || got.InTok != 150 || got.OutTok != 30 {
		t.Fatalf("Sum = %+v", got)
	}
}

func TestAddressedRateZeroPosted(t *testing.T) {
	if (CategoryStat{}).AddressedRate() != 0 {
		t.Fatal("empty category rate must be 0")
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden (run `make golden`): %v", err)
	}
	if string(want) != got {
		t.Errorf("golden mismatch %s:\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
