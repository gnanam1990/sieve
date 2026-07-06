package webhook

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryLogTrimEvictsOldest(t *testing.T) {
	d, err := openDeliveryLog(t.TempDir(), 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"g1", "g2", "g3", "g4"} {
		if err := d.record(g); err != nil {
			t.Fatal(err)
		}
	}
	if d.has("g1") {
		t.Error("g1 should have been evicted (capacity 3)")
	}
	for _, g := range []string{"g2", "g3", "g4"} {
		if !d.has(g) {
			t.Errorf("%s should still be present", g)
		}
	}
}

func TestDeliveryLogLRURecency(t *testing.T) {
	d, err := openDeliveryLog(t.TempDir(), 3)
	if err != nil {
		t.Fatal(err)
	}
	d.record("g1") //nolint:errcheck
	d.record("g2") //nolint:errcheck
	d.record("g3") //nolint:errcheck
	// Touch g1 so it becomes most-recent; g2 is now the oldest.
	if !d.has("g1") {
		t.Fatal("g1 must be present")
	}
	d.record("g4") //nolint:errcheck // evicts the oldest (g2)
	if d.has("g2") {
		t.Error("g2 should have been evicted after g1 was refreshed")
	}
	if !d.has("g1") {
		t.Error("g1 was refreshed and must survive")
	}
}

func TestDeliveryLogRotationBoundsFile(t *testing.T) {
	dir := t.TempDir()
	d, err := openDeliveryLog(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Record 3× the capacity to trigger at least one rotation.
	for i := 0; i < 9; i++ {
		if err := d.record(guidN(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	// After rotation the file must hold at most `cap` live ids, not all 9.
	data, err := os.ReadFile(filepath.Join(dir, "deliveries.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines > 3 {
		t.Errorf("rotation should bound the file to ~cap lines, got %d", lines)
	}
	// The last three ids must still dedupe after a reopen.
	d2, err := openDeliveryLog(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 6; i < 9; i++ {
		if !d2.has(guidN(i)) {
			t.Errorf("recent id %s must survive rotation+reload", guidN(i))
		}
	}
}

func TestDeliveryLogLoadTrims(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deliveries.jsonl")
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(`{"id":"` + guidN(i) + `"}` + "\n")
	}
	b.WriteString("garbage line\n") // tolerated
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := openDeliveryLog(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if d.order.Len() != 3 {
		t.Fatalf("load must trim to capacity, got %d", d.order.Len())
	}
	if d.has(guidN(0)) {
		t.Error("oldest loaded id should be trimmed")
	}
	if !d.has(guidN(5)) {
		t.Error("newest loaded id should be kept")
	}
}

func guidN(i int) string { return "guid-" + string(rune('a'+i)) }

// TestDeliveryLogRecordExistingIsNoop: recording an already-known id just
// refreshes recency, it doesn't duplicate.
func TestDeliveryLogRecordExistingIsNoop(t *testing.T) {
	d, err := openDeliveryLog(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.record("g1"); err != nil {
		t.Fatal(err)
	}
	if err := d.record("g1"); err != nil { // duplicate → no-op
		t.Fatal(err)
	}
	if d.order.Len() != 1 {
		t.Fatalf("recording an existing id must not duplicate, len=%d", d.order.Len())
	}
}

func TestOpenDeliveryLogBadDir(t *testing.T) {
	// A regular file where a directory is expected makes MkdirAll fail.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDeliveryLog(filepath.Join(f, "sub"), 4); err == nil {
		t.Fatal("openDeliveryLog under a file must error")
	}
}

func TestDeliveryLogLoadSkipsEmptyID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deliveries.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":""}`+"\n"+`{"id":"real"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := openDeliveryLog(dir, 4)
	if err != nil {
		t.Fatal(err)
	}
	if d.order.Len() != 1 || !d.has("real") {
		t.Fatalf("empty id must be skipped, only 'real' kept; len=%d", d.order.Len())
	}
}

// TestWebhookNoDeliveryHeader: a request with no X-GitHub-Delivery is still
// processed (nothing to dedupe against) and not recorded.
func TestWebhookNoDeliveryHeader(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "opened", "org/repo", 1, "s", false)
	w := post(t, h, "pull_request", "", body, sign(testSecret, body))
	if w.Code != 202 {
		t.Fatalf("no-delivery request should still enqueue, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 enqueue, got %d", rec.count())
	}
}

func TestNewRequiresSecret(t *testing.T) {
	_, err := New(Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, t.TempDir())
	if err == nil {
		t.Fatal("empty secret must error")
	}
}

func TestNewBadDataDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{Secret: testSecret, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, filepath.Join(f, "sub"))
	if err == nil {
		t.Fatal("New under a file path must error")
	}
}

// TestWebhookRecordPersistError: a delivery-log write failure is best-effort —
// the request still succeeds (200/202), it just isn't persisted.
func TestWebhookRecordPersistError(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	if err := h.dedupe.close(); err != nil { // force subsequent persist writes to fail
		t.Fatal(err)
	}
	body := prBody(t, "opened", "org/repo", 1, "s", false)
	w := post(t, h, "pull_request", "d1", body, sign(testSecret, body))
	if w.Code != 202 {
		t.Fatalf("persist failure must not fail the request, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("job should still enqueue, got %d", rec.count())
	}
}
