package processing

// Abstention gate (P4 — FR7).
//
// The gate applies FR16's calibrated threshold against the ranker's
// OWN score curve (DESIGN §3): one curve, not two independently-tuned
// ones. FR16's calibration job lands in P5; this file takes the
// threshold as a parameter — it never invents one. NaN fails closed
// (abstain everything): an uninterpretable threshold is no basis for
// returning records. Scores exactly at threshold clear it.

// ApplyThreshold splits scored candidates into the returned set.
// abstained is true when nothing clears the threshold — the caller
// returns ABSTAIN, never best-effort noise (FR7). Empty input
// abstains. Output preserves DedupBySubject order (score desc).
func ApplyThreshold(in []Scored, threshold float64) (kept []Scored, abstained bool) {
	if len(in) == 0 {
		return nil, true
	}
	if isNaNThreshold(threshold) {
		return nil, true
	}
	for _, s := range in {
		if s.Score >= threshold {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return nil, true
	}
	return kept, false
}

func isNaNThreshold(f float64) bool { return f != f }
