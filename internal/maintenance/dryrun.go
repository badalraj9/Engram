// Tick preview (R0 — operator-surface risk-close, BUILD-PLAN item 2):
// answers "what would the next tick collect" with ZERO writes — no
// Restatus, no Append, no CommitTick, no Sync. The preview shares ONE
// classification with the real sweep (classifyWindows / pruneVictims
// are the exact code expireAt / pruneAt execute), so the preview can
// never drift from what a real tick would do. Calibration runs
// read-only to report whether E2 economics are armed: on sub-sample
// corpora (MinCalSamples) nothing retires by construction.
//
// R0 gates: a preview leaves the store byte-identical (durable tick
// mark unchanged, replay unchanged) and consumes nothing — a real
// tick after a preview collects exactly what the preview named.
package maintenance

import (
	"engram/internal/index"
	"engram/internal/truth"
)

// TickPreview is one read-only projection of the next tick at wallNow.
type TickPreview struct {
	// E1 — the validity sweep would close these EPISODIC versions
	// (valid_to <= wallNow), sorted.
	ExpiredIDs []string
	// E1 — the sweep would wake these SCHEDULED versions (window
	// opened, still unexpired), sorted.
	ActivatedIDs []string
	// E2 — economics would close these (empty when thresholds are
	// uncalibrated: sub-sample corpora retire nothing by
	// construction).
	RetiredIDs []string
	// FR16 state: whether the corpus cleared MinCalSamples (E2
	// armed/disarmed) and the thresholds the next tick would apply.
	Calibrated       bool
	AbstainThreshold float64
	EvictThreshold   float64
}

// Preview classifies the next tick's collection against an explicit
// wall-clock (daemon-stamped, same authority as the tick's now). It
// performs no writes and commits no tick mark: callers can preview
// repeatedly and the store stays untouched.
func Preview(store *truth.Store, idx *index.Index, wallNow int64) (TickPreview, error) {
	live := liveIndex(store, idx)
	expired, activated := classifyWindows(live, wallNow)
	tail, err := store.TailOffset()
	if err != nil {
		return TickPreview{}, err
	}
	th, err := calibrateAt(store, int64(tail))
	if err != nil {
		return TickPreview{}, err
	}
	var retired []string
	if th.Calibrated {
		retired, err = pruneVictims(store, live, th.Evict, int64(tail), RetireHorizon)
		if err != nil {
			return TickPreview{}, err
		}
	}
	return TickPreview{
		ExpiredIDs:       expired,
		ActivatedIDs:     activated,
		RetiredIDs:       retired,
		Calibrated:       th.Calibrated,
		AbstainThreshold: th.Abstain,
		EvictThreshold:   th.Evict,
	}, nil
}
