package bff

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Metrics is a tiny dependency-free Prometheus text-format registry (counters only). Swap for an
// OpenTelemetry meter later without touching the middleware: everything goes through Inc/Add.
type Metrics struct {
	mu sync.Mutex
	c  map[string]float64
}

// NewMetrics returns an empty registry.
func NewMetrics() *Metrics { return &Metrics{c: map[string]float64{}} }

// Inc adds 1 to the counter name{labels...}. labels are key,value pairs.
func (m *Metrics) Inc(name string, labels ...string) { m.Add(name, 1, labels...) }

// Add adds v to a counter. Safe on a nil receiver.
func (m *Metrics) Add(name string, v float64, labels ...string) {
	if m == nil {
		return
	}
	var b strings.Builder
	b.WriteString(name)
	for i := 0; i+1 < len(labels); i += 2 {
		if i == 0 {
			b.WriteByte('{')
		} else {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", labels[i], labels[i+1])
	}
	if len(labels) > 0 {
		b.WriteByte('}')
	}
	m.mu.Lock()
	m.c[b.String()] += v
	m.mu.Unlock()
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		keys := make([]string, 0, len(m.c))
		for k := range m.c {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s %g\n", k, m.c[k])
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(b.String()))
	})
}

// Instrument counts requests and total latency by method and status class.
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		st := ww.Status()
		if st == 0 {
			st = http.StatusOK
		}
		cls := fmt.Sprintf("%dxx", st/100)
		m.Inc("bff_http_requests_total", "method", r.Method, "status", cls)
		m.Add("bff_http_request_seconds_sum", time.Since(start).Seconds(), "method", r.Method)
	})
}
