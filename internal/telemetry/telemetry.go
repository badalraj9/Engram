// Package telemetry is Engram's cross-cutting observability (TECH §2.9).
//
// Structured logging over log/slog plus an in-process counter/histogram
// registry feeding NFR1's p99 measurement. No external logging or
// metrics library (stdlib-first frozen rule). The core runs with a nil
// registry — telemetry is attached, never required: every method
// nil-guards, so a nil *Registry is a valid silent registry.
//
// Exposure: metrics render as Prometheus text on demand (for external
// monitoring); the `inspect` RPC op is deferred post-v1 (SRS §3), so in
// P5 there is no wire exposure — the server scrapes the registry into
// its own logs. This is additive, never a dependency of any hot path.
package telemetry

import (
	"log/slog"
	"os"
	"sync"
)

// Registry is the in-process metrics registry. Zero value is usable
// but prefer New; a nil *Registry is a silent no-op registry.
type Registry struct {
	mu       sync.Mutex
	counters map[string]int64
	sums     map[string]float64
}

// New returns a ready registry.
func New() *Registry {
	return &Registry{counters: make(map[string]int64), sums: make(map[string]float64)}
}

// Inc adds one to a counter. Nil-safe.
func (r *Registry) Inc(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters == nil {
		r.counters = make(map[string]int64)
	}
	r.counters[name]++
}

// Add adds n to a counter. Nil-safe.
func (r *Registry) Add(name string, n int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters == nil {
		r.counters = make(map[string]int64)
	}
	r.counters[name] += n
}

// Observe folds a latency sample into a sum + count pair (p99 math
// consumes the raw samples at scrape time in P6; P5 keeps sums only —
// stated so the histogram shape is not oversold). Nil-safe.
func (r *Registry) Observe(name string, seconds float64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sums == nil {
		r.sums = make(map[string]float64)
	}
	if r.counters == nil {
		r.counters = make(map[string]int64)
	}
	r.sums[name+"_sum"] += seconds
	r.counters[name+"_count"]++
}

// Get returns a counter value (0 when absent). Nil-safe.
func (r *Registry) Get(name string) int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[name]
}

// Logger returns the process structured logger (log/slog, stderr,
// text handler). One shared instance — loggers are not per-request.
func Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// Operation counter names (the per-op latency + verdict surface P5's
// server records; abstain/consolidation/pruning/calibration counts
// feed the dogfood verdict through the server's own logs).
const (
	OpEmit            = "engram_emit_total"
	OpRecall          = "engram_recall_total"
	OpCorrect         = "engram_correct_total"
	OpAttest          = "engram_attest_total"
	OpResolve         = "engram_resolve_total"
	OpAsOf            = "engram_asof_total"
	OpConsolidate     = "engram_consolidate_total"
	AbstainTotal      = "engram_abstain_total"
	VerdictPrefix     = "engram_verdict_"
	PrunedTotal       = "engram_pruned_total"
	ConsolidatedTotal = "engram_consolidated_total"
	LatencySuffix     = "_latency_seconds"
)
