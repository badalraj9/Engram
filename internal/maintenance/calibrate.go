// FR16 calibration (P5 — one mechanism, three consumers).
//
// A single calibration pass derives all three thresholds from the
// live value distribution on a rolling window: recall abstention
// (FR7, on the FR6 score scale — unbounded ≥ 0, DESIGN §3), pruning
// eviction (FR11, same score scale), and consolidation acceptance
// (FR10, on the probe-distortion scale [0,1] — different consumer,
// different scale, same pass).
//
// v1 simplification, stated honestly: FR16's full ablation protocol
// (remove a record, observe behavior change against a workload) needs
// a dogfood workload that does not exist yet (UC5 → P6). Until then
// the pass measures the live value distribution — value =
// trust × R_m(now) at hits=1, corr=0 — and cuts quantiles: abstain at
// p25 (filters the bottom quartile), evict at p10, accept fixed at
// 0.5 distortion. The mechanism shape (one pass, three outputs,
// rolling window, score-scale abstain/evict) is what P6's workload
// plugs into — recalibration changes the probe, never the plumbing.
// Empty store returns the config defaults (abstain 0.5).
package maintenance

import (
	"sort"

	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Threshold defaults: abstain matches config.Default (0.5, score
// scale); evict sits below it (pruning is stricter than abstaining —
// an abstained record may still be worth keeping); accept is the
// probe-distortion ceiling [0,1] (D_probe <= Accept consolidates).
const (
	DefaultAbstain = 0.5
	DefaultEvict   = 0.1
	DefaultAccept  = 0.5
)

// CalWindow is the rolling-window depth in commit sequences: only
// versions born within [tail-CalWindow, tail) calibrate (NFR10:
// recalibrated on a rolling window as data drifts). v1-fixed
// provisional, same discipline as RecencyHalfLife.
const CalWindow = 100000

// Thresholds is FR16's single output serving three call sites.
type Thresholds struct {
	Abstain float64 // recall floor, FR6 score scale, >= 0
	Accept  float64 // consolidation ceiling, distortion [0,1]
	Evict   float64 // pruning floor, FR6 score scale, >= 0
}

// Defaults returns the empty-store thresholds.
func Defaults() Thresholds {
	return Thresholds{Abstain: DefaultAbstain, Accept: DefaultAccept, Evict: DefaultEvict}
}

// Calibrate samples live EPISODIC value on the rolling window and cuts
// quantiles. Score scale for abstain/evict (shared with the ranker —
// DESIGN §3); distortion scale for accept. Deterministic: same log
// yields same thresholds. now is the tick's frozen clock (the tick
// threads one `now` through calibrate and prune — reading the tail
// twice would let an appended summary shift retrievability under the
// just-cut floor by a sequence step; found by TestPrune_Invariant).
func Calibrate(store *truth.Store) (Thresholds, error) {
	tail, err := store.TailOffset()
	if err != nil {
		return Thresholds{}, err
	}
	return calibrateAt(store, int64(tail))
}

// calibrateAt runs the pass against a frozen now.
func calibrateAt(store *truth.Store, now int64) (Thresholds, error) {
	floor := int64(0)
	if now > CalWindow {
		floor = now - CalWindow
	}
	var values []float64
	seen := make(map[string]bool)
	err := store.Scan(0, func(ev truth.Event) error {
		if ev.Kind != truth.EventRecord {
			return nil
		}
		r := ev.Record
		if r.Type != rpc.RecordEpisodic || r.Status == rpc.StatusTombstoned {
			return nil
		}
		if seen[r.ID] {
			return nil
		}
		seen[r.ID] = true
		if r.Txn.From < floor {
			return nil
		}
		fresh, err := store.Get(r.ID)
		if err != nil {
			return nil // gone since: absent, never partial
		}
		if fresh.Status == rpc.StatusTombstoned || fresh.Type != rpc.RecordEpisodic {
			return nil
		}
		values = append(values, baselineValue(fresh, now))
		return nil
	})
	if err != nil {
		return Thresholds{}, err
	}
	if len(values) == 0 {
		return Defaults(), nil
	}
	sort.Float64s(values)
	return Thresholds{
		Abstain: quant(values, 0.25),
		Accept:  DefaultAccept,
		Evict:   quant(values, 0.10),
	}, nil
}

// baselineValue is the FR6 curve at hits=1, corr=0: trust × R_m(now).
// Hits and corroboration vary per query — calibration floors on the
// record's intrinsic retrievability, never on a particular query.
func baselineValue(r rpc.Record, now int64) float64 {
	return processing.TrustWeight(r.ACL) * processing.Retrievability(now, r.Txn.From, 0)
}

// quant returns the q-quantile over sorted values (clamped ≥ 0 —
// score scale has 0 as its floor, no ceiling).
func quant(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	if sorted[i] < 0 {
		return 0
	}
	return sorted[i]
}
