// Package metrics is a small, allocation-free instrumentation library:
// counters, gauges and fixed-bucket latency histograms, exported in the
// Prometheus text format together with Go runtime metrics. Debug servers
// also expose net/http/pprof.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Counter struct{ v atomic.Int64 }

func (c *Counter) Add(n int64) { c.v.Add(n) }
func (c *Counter) Inc()        { c.v.Add(1) }
func (c *Counter) Value() int64 {
	return c.v.Load()
}

type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Set(n int64) { g.v.Store(n) }
func (g *Gauge) Add(n int64) { g.v.Add(n) }
func (g *Gauge) Value() int64 {
	return g.v.Load()
}

// Bucket bounds in seconds: 10µs … ~3h, four buckets per doubling (≈19%
// relative resolution).
var bounds = func() []float64 {
	var b []float64
	f := math.Pow(2, 0.25)
	for v := 10e-6; v < 10800; v *= f {
		b = append(b, v)
	}
	return b
}()

// Histogram records durations.
type Histogram struct {
	counts []atomic.Int64
	sumNs  atomic.Int64
	n      atomic.Int64
	maxNs  atomic.Int64
}

func newHistogram() *Histogram { return &Histogram{counts: make([]atomic.Int64, len(bounds)+1)} }

func (h *Histogram) Observe(d time.Duration) {
	s := d.Seconds()
	i := sort.SearchFloat64s(bounds, s)
	h.counts[i].Add(1)
	h.sumNs.Add(int64(d))
	h.n.Add(1)
	for {
		m := h.maxNs.Load()
		if int64(d) <= m || h.maxNs.CompareAndSwap(m, int64(d)) {
			break
		}
	}
}

// Since observes the time elapsed since t.
func (h *Histogram) Since(t time.Time) { h.Observe(time.Since(t)) }

// Quantile estimates q (0..1), interpolating linearly inside a bucket.
func (h *Histogram) Quantile(q float64) time.Duration {
	n := h.n.Load()
	if n == 0 {
		return 0
	}
	maxD := time.Duration(h.maxNs.Load())
	target := q * float64(n)
	var acc float64
	for i := range h.counts {
		c := float64(h.counts[i].Load())
		if c > 0 && acc+c >= target {
			if i >= len(bounds) {
				return maxD
			}
			lo := 0.0
			if i > 0 {
				lo = bounds[i-1]
			}
			v := lo + (bounds[i]-lo)*(target-acc)/c
			return min(time.Duration(v*float64(time.Second)), maxD)
		}
		acc += c
	}
	return maxD
}

func (h *Histogram) Count() int64 { return h.n.Load() }
func (h *Histogram) Mean() time.Duration {
	n := h.n.Load()
	if n == 0 {
		return 0
	}
	return time.Duration(h.sumNs.Load() / n)
}

// Registry holds named metrics. Names may carry Prometheus labels, e.g.
// `hermesh_rpc_seconds{method="StartSession"}`.
type Registry struct {
	mu         sync.Mutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	gaugeFuncs map[string]func() int64
	hists      map[string]*Histogram
}

func NewRegistry() *Registry {
	return &Registry{counters: map[string]*Counter{}, gauges: map[string]*Gauge{}, gaugeFuncs: map[string]func() int64{}, hists: map[string]*Histogram{}}
}

// Default is the process-wide registry.
var Default = NewRegistry()

func (r *Registry) Counter(name string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.counters[name]
	if c == nil {
		c = &Counter{}
		r.counters[name] = c
	}
	return c
}

func (r *Registry) Gauge(name string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.gauges[name]
	if g == nil {
		g = &Gauge{}
		r.gauges[name] = g
	}
	return g
}

// GaugeFunc registers a gauge computed at scrape time.
func (r *Registry) GaugeFunc(name string, f func() int64) {
	r.mu.Lock()
	r.gaugeFuncs[name] = f
	r.mu.Unlock()
}

func (r *Registry) Histogram(name string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hists[name]
	if h == nil {
		h = newHistogram()
		r.hists[name] = h
	}
	return h
}

// Snapshot returns histograms by name (for in-process reports).
func (r *Registry) Histograms() map[string]*Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*Histogram, len(r.hists))
	for k, v := range r.hists {
		out[k] = v
	}
	return out
}

func splitName(n string) (base, labels string) {
	if i := strings.IndexByte(n, '{'); i >= 0 {
		return n[:i], n[i+1 : len(n)-1]
	}
	return n, ""
}

func withLabel(labels, extra string) string {
	if labels == "" {
		return "{" + extra + "}"
	}
	return "{" + labels + "," + extra + "}"
}

// WritePrometheus writes all metrics plus Go runtime and process metrics.
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.Lock()
	names := func(m map[string]bool) []string {
		var s []string
		for k := range m {
			s = append(s, k)
		}
		sort.Strings(s)
		return s
	}
	cs, gs, gfs, hs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for k := range r.counters {
		cs[k] = true
	}
	for k := range r.gauges {
		gs[k] = true
	}
	for k := range r.gaugeFuncs {
		gfs[k] = true
	}
	for k := range r.hists {
		hs[k] = true
	}
	r.mu.Unlock()
	for _, n := range names(cs) {
		fmt.Fprintf(w, "%s %d\n", n, r.Counter(n).Value())
	}
	for _, n := range names(gs) {
		fmt.Fprintf(w, "%s %d\n", n, r.Gauge(n).Value())
	}
	for _, n := range names(gfs) {
		r.mu.Lock()
		f := r.gaugeFuncs[n]
		r.mu.Unlock()
		fmt.Fprintf(w, "%s %d\n", n, f())
	}
	for _, n := range names(hs) {
		h := r.Histogram(n)
		base, labels := splitName(n)
		var acc int64
		for i, b := range bounds {
			acc += h.counts[i].Load()
			fmt.Fprintf(w, "%s_bucket%s %d\n", base, withLabel(labels, fmt.Sprintf(`le="%g"`, b)), acc)
		}
		acc += h.counts[len(bounds)].Load()
		fmt.Fprintf(w, "%s_bucket%s %d\n", base, withLabel(labels, `le="+Inf"`), acc)
		lb := ""
		if labels != "" {
			lb = "{" + labels + "}"
		}
		fmt.Fprintf(w, "%s_sum%s %g\n%s_count%s %d\n", base, lb, float64(h.sumNs.Load())/1e9, base, lb, h.n.Load())
	}
	writeRuntime(w)
}

var runtimeSamples = []string{
	"/sched/goroutines:goroutines",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/total:bytes",
	"/gc/cycles/total:gc-cycles",
	"/cpu/classes/total:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
}

func writeRuntime(w io.Writer) {
	s := make([]metrics.Sample, len(runtimeSamples))
	for i, n := range runtimeSamples {
		s[i].Name = n
	}
	metrics.Read(s)
	for _, x := range s {
		name := "go" + strings.NewReplacer("/", "_", ":", "_", "-", "_").Replace(x.Name)
		switch x.Value.Kind() {
		case metrics.KindUint64:
			fmt.Fprintf(w, "%s %d\n", name, x.Value.Uint64())
		case metrics.KindFloat64:
			fmt.Fprintf(w, "%s %g\n", name, x.Value.Float64())
		}
	}
	fmt.Fprintf(w, "process_open_fds %d\n", openFDs())
	fmt.Fprintf(w, "go_gomaxprocs %d\n", runtime.GOMAXPROCS(0))
}

func openFDs() int {
	for _, dir := range []string{"/dev/fd", "/proc/self/fd"} {
		if f, err := os.Open(dir); err == nil {
			names, _ := f.Readdirnames(-1)
			f.Close()
			return len(names) - 1
		}
	}
	return -1
}

// Handler serves /metrics and /debug/pprof/*.
func Handler(r *Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		r.WritePrometheus(w)
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Serve starts the debug server on addr (only call with a loopback addr).
func Serve(addr string) error {
	// Block and mutex profiling cost CPU on every contended operation;
	// enable them only when asked for.
	if os.Getenv("HERMESH_PROFILE_BLOCKING") == "1" {
		runtime.SetMutexProfileFraction(5)
		runtime.SetBlockProfileRate(int(time.Millisecond))
	}
	return http.ListenAndServe(addr, Handler(Default))
}
