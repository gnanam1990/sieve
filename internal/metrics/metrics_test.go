package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCounter(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("sieve_reviews_total")
	c.Inc(map[string]string{"pipeline": "single", "outcome": "ok"})
	c.Inc(map[string]string{"pipeline": "single", "outcome": "ok"})
	c.Inc(map[string]string{"pipeline": "judge", "outcome": "error"})

	out := r.Serialize()
	if !strings.Contains(out, `sieve_reviews_total{outcome="ok",pipeline="single"} 2`) {
		t.Errorf("expected single/ok=2 in output:\n%s", out)
	}
	if !strings.Contains(out, `sieve_reviews_total{outcome="error",pipeline="judge"} 1`) {
		t.Errorf("expected judge/error=1 in output:\n%s", out)
	}
}

func TestHistogram(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("sieve_review_duration_seconds", []float64{0.1, 1, 10})
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(5.0)

	out := r.Serialize()
	for _, want := range []string{
		`# TYPE sieve_review_duration_seconds histogram`,
		`sieve_review_duration_seconds_bucket{le="0.1"} 1`,
		`sieve_review_duration_seconds_bucket{le="1"} 2`,
		`sieve_review_duration_seconds_bucket{le="10"} 3`,
		`sieve_review_duration_seconds_bucket{le="+Inf"} 3`,
		`sieve_review_duration_seconds_count 3`,
		`sieve_review_duration_seconds_sum 5.55`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGauge(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("sieve_queue_depth")
	g.Set(7)
	g.Add(2)

	out := r.Serialize()
	if !strings.Contains(out, "sieve_queue_depth 9") {
		t.Errorf("expected gauge value 9 in output:\n%s", out)
	}
}

func TestHandler(t *testing.T) {
	r := NewRegistry()
	r.Gauge("sieve_workers").Set(4)

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sieve_workers 4") {
		t.Errorf("expected workers gauge in handler output:\n%s", body)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}
}

