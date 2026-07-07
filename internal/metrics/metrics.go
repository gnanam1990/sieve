// Package metrics provides a minimal Prometheus-compatible metrics registry for
// sieve serve. It deliberately does not pull in the Prometheus client library:
// sieve is a single static binary and this package is small enough to maintain
// in-house while still producing text/exposition format that Prometheus can
// scrape.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry holds the daemon's metrics. It is safe for concurrent use.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*counter
	histograms map[string]*histogram
	gauges     map[string]*gauge
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]*counter{},
		histograms: map[string]*histogram{},
		gauges:     map[string]*gauge{},
	}
}

// Counter returns a counter metric, creating it if necessary. name must be
// a valid Prometheus metric name; labels are sorted for stable output.
func (r *Registry) Counter(name string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[name]
	if !ok {
		c = &counter{name: name}
		r.counters[name] = c
	}
	return &Counter{c: c}
}

// Histogram returns a histogram metric, creating it if necessary.
// buckets are the upper bounds in seconds (or whatever unit the caller observes).
func (r *Registry) Histogram(name string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.histograms[name]
	if !ok {
		h = &histogram{name: name, buckets: buckets}
		r.histograms[name] = h
	}
	return &Histogram{h: h}
}

// Gauge returns a gauge metric, creating it if necessary.
func (r *Registry) Gauge(name string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gauges[name]
	if !ok {
		g = &gauge{name: name}
		r.gauges[name] = g
	}
	return &Gauge{g: g}
}

// Handler serves Prometheus text exposition format at /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.Serialize()))
	})
}

// Serialize returns the current metrics in Prometheus text format.
func (r *Registry) Serialize() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	names := make([]string, 0, len(r.counters)+len(r.histograms)+len(r.gauges))
	for n := range r.counters {
		names = append(names, n)
	}
	for n := range r.histograms {
		names = append(names, n)
	}
	for n := range r.gauges {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		switch {
		case r.counters[name] != nil:
			r.counters[name].write(&b)
		case r.histograms[name] != nil:
			r.histograms[name].write(&b)
		case r.gauges[name] != nil:
			r.gauges[name].write(&b)
		}
	}
	return b.String()
}

// Counter is a reference to a counter metric in the registry.
type Counter struct {
	c *counter
}

// Inc increments the counter by one for the given label set.
func (c *Counter) Inc(labels map[string]string) {
	c.c.add(labels, 1)
}

// Add increments the counter by delta for the given label set.
func (c *Counter) Add(labels map[string]string, delta int64) {
	c.c.add(labels, delta)
}

// counter is the internal counter implementation.
type counter struct {
	name string
	mu   sync.RWMutex
	vals map[string]int64 // serialized label string -> count
}

func (c *counter) add(labels map[string]string, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = map[string]int64{}
	}
	c.vals[serializeLabels(labels)] += delta
}

func (c *counter) write(b *strings.Builder) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	fmt.Fprintf(b, "# HELP %s %s total\n# TYPE %s counter\n", c.name, c.name, c.name)
	if len(c.vals) == 0 {
		fmt.Fprintf(b, "%s 0\n", c.name)
		return
	}
	keys := make([]string, 0, len(c.vals))
	for k := range c.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" {
			fmt.Fprintf(b, "%s %d\n", c.name, c.vals[k])
		} else {
			fmt.Fprintf(b, "%s{%s} %d\n", c.name, k, c.vals[k])
		}
	}
}

// Histogram is a reference to a histogram metric in the registry.
type Histogram struct {
	h *histogram
}

// Observe records a value (e.g. duration in seconds).
func (h *Histogram) Observe(value float64) {
	h.h.observe(value)
}

// histogram is the internal histogram implementation with fixed buckets.
type histogram struct {
	name    string
	buckets []float64
	mu      sync.RWMutex
	counts  map[string]int64 // bucket label -> count
	sum     float64
	count   int64
}

func (h *histogram) observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.counts == nil {
		h.counts = map[string]int64{}
	}
	h.sum += value
	h.count++
	for _, b := range h.buckets {
		if value <= b {
			h.counts[fmt.Sprintf("le=\"%g\"", b)]++
		}
	}
	h.counts[`le="+Inf"`]++
}

func (h *histogram) write(b *strings.Builder) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fmt.Fprintf(b, "# HELP %s %s in seconds\n# TYPE %s histogram\n", h.name, h.name, h.name)
	keys := make([]string, 0, len(h.counts))
	for k := range h.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s_bucket{%s} %d\n", h.name, k, h.counts[k])
	}
	fmt.Fprintf(b, "%s_sum %g\n", h.name, h.sum)
	fmt.Fprintf(b, "%s_count %d\n", h.name, h.count)
}

// Gauge is a reference to a gauge metric in the registry.
type Gauge struct {
	g *gauge
}

// Set updates the gauge value.
func (g *Gauge) Set(value float64) {
	g.g.set(value)
}

// Add atomically adds delta to the gauge.
func (g *Gauge) Add(delta float64) {
	g.g.add(delta)
}

type gauge struct {
	name string
	mu   sync.RWMutex
	val  float64
}

func (g *gauge) set(value float64) {
	g.mu.Lock()
	g.val = value
	g.mu.Unlock()
}

func (g *gauge) add(delta float64) {
	g.mu.Lock()
	g.val += delta
	g.mu.Unlock()
}

func (g *gauge) write(b *strings.Builder) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.name, g.name)
	fmt.Fprintf(b, "%s %g\n", g.name, g.val)
}

// serializeLabels sorts label keys and emits Prometheus label syntax.
func serializeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=\"%s\"", k, escape(labels[k])))
	}
	return strings.Join(pairs, ",")
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// Now returns the current time; overridable in tests.
var Now = time.Now
