// Serialized tick (P5 — FR10, DESIGN §6, BUILD-PLAN P5 exit).
//
// One maintenance pass performs, IN THIS ORDER, serialized within a
// single host-supplied tick: sweep (invariant-audit) → calibrate →
// consolidate → expire (validity sweep, P2 E1) → prune (economics,
// P2 E2, calibrated-gated). No timer, no time.Ticker, no background
// goroutine (frozen FR10) — the host owns the clock; this package owns
// the ordering. The file imports no time package by construction:
// wall-now arrives as a daemon-stamped parameter.
//
// Tick durability: the host counter is monotonic; Engram remembers the
// high-water mark (M/tick, truth/tick.go) and rejects tick <= last, so
// a daemon restart cannot re-run an old tick. The mark commits in the
// tick's Sync boundary — ack only after Sync (frozen ack rule).
//
// Sweep first: the DESIGN §4.10 invariant-audit routes pre-existing
// diff-object overlaps outside BOTH-state to BOTH BEFORE consolidation
// reads, so a summary never bakes an unresolved conflict in.
// Consolidation clusters ACTIVE EPISODIC only (BOTH/TOMBSTONED/
// SCHEDULED never synthesize — conflicts, history, and future events
// are not summary material).
package maintenance

import (
	"fmt"
	"sort"
	"strings"

	"engram/internal/index"
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// SummaryMaxObjects caps the distinct-object list a synthesized
// summary carries: the object field stays bounded and D_probe stays
// meaningful (truncation past the cap IS distortion, measured, never
// hidden). Payload-adjacent bound, same spirit as the 64-key cap.
const SummaryMaxObjects = 16

// Result is one tick's audited outcome.
type Result struct {
	Tick         int64
	Swept        int
	Consolidated int
	Pruned       int
	// Expired/Retired split Pruned by cause (P2 contract): validity
	// sweep vs economics retirement. Pruned stays the wire-compatible
	// total; the split surfaces on holder stderr now, wire fields at
	// Q4. Internal only.
	Expired    int
	Retired    int
	Thresholds Thresholds
}

// Consolidate runs one serialized tick: sweep → calibrate → consolidate →
// expire → prune, then commits the tick mark and Syncs once. tick <= last
// fails closed with no mutation. The serving index is updated inline
// (Add new versions, Retire closed ones) — under the server's single
// mutex, never concurrently.
//
// Two clocks, one per domain (P2): the commit-sequence tail drives
// economics (retrievability breathes in seq steps — RecencyHalfLife),
// while wallNow drives validity (windows are world time in
// unix-nanos — DESIGN §1). wallNow arrives as an explicit parameter,
// daemon-stamped like recall now: maintenance stays time-free, the
// clock lives where the authority does.
func Consolidate(store *truth.Store, idx *index.Index, tick, wallNow int64) (Result, error) {
	last, err := store.LastTick()
	if err != nil {
		return Result{}, err
	}
	if tick <= last {
		return Result{}, rpc.E(rpc.CodeInvalidRecord, "maintenance: tick must exceed last accepted tick")
	}
	var res Result
	res.Tick = tick
	// One clock for the whole tick (see calibrateAt): frozen before
	// the sweep appends, so retrievability cannot drift mid-pass.
	tail, err := store.TailOffset()
	if err != nil {
		return Result{}, err
	}
	now := int64(tail)
	swept, err := sweep(store, idx)
	if err != nil {
		return Result{}, err
	}
	res.Swept = swept
	th, err := calibrateAt(store, now)
	if err != nil {
		return Result{}, err
	}
	cons, err := consolidate(store, idx, tick, th.Accept)
	if err != nil {
		return Result{}, err
	}
	res.Consolidated = cons
	// P2 E1: validity sweep closes expired windows — the tick as
	// closer, before economics runs.
	expired, err := expireAt(store, idx, wallNow)
	if err != nil {
		return Result{}, err
	}
	for _, id := range expired {
		idx.Retire(id)
	}
	res.Expired = len(expired)
	// P2 E2: economics retirement fires only on calibrated thresholds
	// — sub-sample corpora retire nothing (conservative by
	// construction). The horizon guard lives in pruneAt.
	var retired []string
	if th.Calibrated {
		retired, err = pruneAt(store, idx, th.Evict, now, RetireHorizon)
		if err != nil {
			return Result{}, err
		}
	}
	for _, id := range retired {
		idx.Retire(id)
	}
	res.Retired = len(retired)
	res.Pruned = len(expired) + len(retired)
	res.Thresholds = th
	if err := store.CommitTick(tick); err != nil {
		return Result{}, err
	}
	// Single Sync covers the tick: ack implies durability (frozen).
	if err := store.Sync(); err != nil {
		return Result{}, err
	}
	// Value-log GC closes the tick (P5-owned entry point): engine
	// hygiene after the logical commit, never before it — a GC
	// failure reports against committed effects (both visible in the
	// return), it never silently un-commits them.
	if err := store.RunValueLogGC(); err != nil {
		return res, err
	}
	return res, nil
}

// sweep audits the DESIGN §4 gate invariant: no two live versions
// under one (subject, predicate) key may carry different objects over
// overlapping valid-time outside BOTH-state. Violations route to BOTH
// via Restatus (defense-in-depth at host cadence — never a substitute
// for the gate). Returns the count flagged.
func sweep(store *truth.Store, idx *index.Index) (int, error) {
	live := liveIndex(store, idx)
	groups := make(map[string][]rpc.Record)
	for _, r := range live {
		if r.Status == rpc.StatusTombstoned || r.Type.Derived() {
			continue
		}
		groups[processing.Key(r.Subject, r.Predicate)] = append(groups[processing.Key(r.Subject, r.Predicate)], r)
	}
	flagged := make(map[string]bool)
	for _, g := range groups {
		for i := 0; i < len(g); i++ {
			for j := i + 1; j < len(g); j++ {
				a, b := g[i], g[j]
				if a.Object == b.Object || !overlap(a.ValidTime, b.ValidTime) {
					continue
				}
				if a.Status == rpc.StatusBoth && b.Status == rpc.StatusBoth {
					continue
				}
				flagged[a.ID] = true
				flagged[b.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(flagged))
	for id := range flagged {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec, err := store.Restatus(id, rpc.StatusBoth)
		if err != nil {
			return 0, err
		}
		idx.Retire(id)
		staged, err := store.ListStaged()
		if err != nil {
			return 0, err
		}
		idx.Add(rec, staged)
	}
	return len(ids), nil
}

// overlap reports half-open interval intersection.
func overlap(a, b rpc.Interval) bool {
	return a.From < b.To && b.From < a.To
}

// consolidate clusters ACTIVE EPISODIC records by (session, subject)
// co-occurrence (DESIGN §6 — no embeddings) and synthesizes one
// SUMMARY per qualifying cluster via deterministic template
// composition (never generation — NFR2's most at-risk boundary).
// Acceptance is gated by the probe-distortion test D_probe <= accept.
func consolidate(store *truth.Store, idx *index.Index, tick int64, accept float64) (int, error) {
	live := liveIndex(store, idx)
	groups := make(map[string][]rpc.Record)
	for _, r := range live {
		if r.Type != rpc.RecordEpisodic || r.Status != rpc.StatusActive {
			continue
		}
		k := r.Provenance.Session + "\x00" + processing.Normalize(r.Subject)
		groups[k] = append(groups[k], r)
	}
	keys := make([]string, 0, len(groups))
	for k, g := range groups {
		if len(g) >= 2 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	// Already-summarized suppression: a cluster whose exact member set
	// a live SUMMARY already covers must not re-derive every tick —
	// without it each tick mints a duplicate summary (found by
	// TestTick_MonotonicNoMutation). Coverage is by exact member-ID
	// set: new members (or tombstoned ones) form a new set and
	// summarize fresh.
	covered, err := coveredSets(store)
	if err != nil {
		return 0, err
	}
	staged, err := store.ListStaged()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		if covered[setKey(memberIDs(groups[k]))] {
			continue
		}
		rec, dprobe := synthesize(groups[k], tick)
		if dprobe > accept {
			continue
		}
		derived, err := store.AppendDerived(recordInput(rec), rpc.RecordSummary, memberIDs(groups[k]))
		if err != nil {
			return n, err
		}
		idx.Add(derived, staged)
		n++
	}
	return n, nil
}

// synthesize composes a SUMMARY from a cluster deterministically:
// weakest-link confidence (FR9 across derived facts), widened
// valid-time, all distinct objects up to SummaryMaxObjects joined in
// sorted order. D_probe is the fraction of distinct member objects
// absent from the summary (truncation past the cap is measured
// distortion, never silent).
func synthesize(members []rpc.Record, tick int64) (rpc.Record, float64) {
	objects := make(map[string]bool)
	var from, to int64
	conf := members[0].BaseConfidence
	for i, m := range members {
		objects[m.Object] = true
		if i == 0 || m.ValidTime.From < from {
			from = m.ValidTime.From
		}
		if i == 0 || m.ValidTime.To > to {
			to = m.ValidTime.To
		}
		if m.BaseConfidence < conf {
			conf = m.BaseConfidence
		}
	}
	distinct := make([]string, 0, len(objects))
	for o := range objects {
		distinct = append(distinct, o)
	}
	sort.Strings(distinct)
	included := distinct
	var missing int
	if len(distinct) > SummaryMaxObjects {
		missing = len(distinct) - SummaryMaxObjects
		included = distinct[:SummaryMaxObjects]
	}
	var dprobe float64
	if len(distinct) > 0 {
		dprobe = float64(missing) / float64(len(distinct))
	}
	// Payload values carry the 4KB cap (rpc.MaxPayloadValLen):
	// truncate the metadata list, never the claim — the object field
	// above already states the counts, so truncation loses detail,
	// never coverage accounting (D_probe is cap-based, unaffected).
	joined := strings.Join(included, " | ")
	if len(joined) > 4000 {
		joined = joined[:4000]
	}
	return rpc.Record{
		Subject:        members[0].Subject,
		Predicate:      "summary",
		Object:         fmt.Sprintf("summary of %s (%d observations, %d distinct)", members[0].Subject, len(members), len(distinct)),
		ValidTime:      rpc.Interval{From: from, To: to},
		Provenance:     rpc.Provenance{Source: "engram", Session: fmt.Sprintf("tick-%d", tick)},
		ACL:            rpc.ACLSystem,
		Status:         rpc.StatusActive,
		Type:           rpc.RecordSummary,
		BaseConfidence: conf,
		Payload:        map[string]string{"objects": joined},
	}, dprobe
}

// memberIDs collects cluster authorship for the DerivedFrom chain
// (rollback + prune-reachability target), sorted.
func memberIDs(members []rpc.Record) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

// setKey renders a sorted member-ID set comparable.
func setKey(ids []string) string {
	return strings.Join(ids, "\x00")
}

// coveredSets returns the member-ID sets live SUMMARY records already
// cover (store-side: summaries never index — see reachableFrom).
func coveredSets(store *truth.Store) (map[string]bool, error) {
	out := make(map[string]bool)
	seen := make(map[string]bool)
	err := store.Scan(0, func(ev truth.Event) error {
		if ev.Kind != truth.EventRecord || ev.Record.Type != rpc.RecordSummary {
			return nil
		}
		if seen[ev.Record.ID] {
			return nil
		}
		seen[ev.Record.ID] = true
		fresh, err := store.Get(ev.Record.ID)
		if err != nil {
			return nil
		}
		if fresh.Status == rpc.StatusTombstoned {
			return nil
		}
		out[setKey(sortedCopy(fresh.DerivedFrom))] = true
		return nil
	})
	return out, err
}

// sortedCopy sorts without mutating the caller's slice.
func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// recordInput shapes a synthesized record for AppendDerived.
func recordInput(r rpc.Record) rpc.InputRecord {
	return rpc.InputRecord{
		Subject:        r.Subject,
		Predicate:      r.Predicate,
		Object:         r.Object,
		ValidTime:      r.ValidTime,
		Provenance:     r.Provenance,
		ACL:            r.ACL,
		BaseConfidence: r.BaseConfidence,
		Payload:        r.Payload,
	}
}
