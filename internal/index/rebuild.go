package index

import (
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Apply folds one log event into the serving index (DESIGN §2
// live-write path: writes arriving past the snapshot boundary apply
// directly against the still-serving index, in log order).
// EventRecord routes through Add (liveness filter included);
// EventFeedback is content-free (FR17: replayed lazily at read by
// P4) and never touches the find structure. Unknown kinds fail
// closed — a postings entry must never point at an uninterpretable
// event.
func (x *Index) Apply(ev truth.Event, staged map[string]bool) error {
	switch ev.Kind {
	case truth.EventRecord:
		x.Add(ev.Record, staged)
		return nil
	case truth.EventFeedback:
		return nil
	default:
		return rpc.E(rpc.CodeCorruptIndex, "index: unknown event kind")
	}
}

// Rebuild demonstrates "rebuildable, not claimed" (DESIGN §2): record
// the log tail as a snapshot boundary, replay [0,tail), then drain
// [tail,now) in log order. Phase-2 drain covers writes that landed
// mid-replay; starting it exactly at the boundary prevents both
// double-apply and silent drop. The caller swaps the returned index
// in atomically — this function allocates the new index and never
// mutates a serving one. Staged arrives as the boundary snapshot set
// (excluded at build, Option A); the durable M/quarantine prefix-scan
// producing that set wires in with P4's single-txn writer — no P1
// edit in P3 (TruthStore iface stays P0-closed). Scan's loud
// truncation propagates: rebuild never papers over a broken prefix.
func Rebuild(store truth.TruthStore, staged map[string]bool) (*Index, error) {
	tail, err := store.TailOffset()
	if err != nil {
		return nil, err
	}
	x := &Index{}
	if err := store.Scan(0, func(ev truth.Event) error {
		if ev.Offset >= tail {
			return nil
		}
		return x.Apply(ev, staged)
	}); err != nil {
		return nil, err
	}
	now, err := store.TailOffset()
	if err != nil {
		return nil, err
	}
	if now > tail {
		if err := store.Scan(tail, func(ev truth.Event) error {
			return x.Apply(ev, staged)
		}); err != nil {
			return nil, err
		}
	}
	return x, nil
}
