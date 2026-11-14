// Trust-floor probe (DESIGN §3 PROPOSED — adversarial pass, owner-run).
//
// This test FAILS NOTHING: it prints the FR6 score of hand-made facts
// across ages so the owner can rule each fate right or wrong. Reply
// with the wrong lines; the pattern sets (or kills) the user-trust
// floor. Read-only math — no store, no mutation.
//
// Shape per line: fact, tier, corroboration, age (seq steps), score,
// fate at the prune floor (0.1, mirroring maintenance.DefaultEvict —
// processing cannot import maintenance, so the value is pinned here
// and must match; drift breaks the comparison, loudly, by inspection).
package processing

import (
	"testing"

	"engram/internal/rpc"
)

const probeEvictFloor = 0.1 // must mirror maintenance.DefaultEvict

type probeFact struct {
	name   string
	tier   rpc.AclTier
	hits   int
	corr   int
	bornAt int64 // txn seq of birth; age = now - bornAt
}

func TestTrustFloor_Probe(t *testing.T) {
	now := int64(1000000)
	facts := []probeFact{
		{"mothers-birthday (user, once, never repeated)", rpc.ACLUser, 1, 0, now - 200000},
		{"mothers-birthday fresh (user, once, just said)", rpc.ACLUser, 1, 0, now - 100},
		{"pump-reading (user, used 3x, corroborated)", rpc.ACLUser, 2, 2, now - 200000},
		{"rumor (low, once, never repeated)", rpc.ACLLow, 1, 0, now - 200000},
		{"rumor fresh (low, once, just said)", rpc.ACLLow, 1, 0, now - 100},
		{"sys-config (system, once, never repeated)", rpc.ACLSystem, 1, 0, now - 200000},
		{"trivia (user, once, never repeated)", rpc.ACLUser, 1, 0, now - 500000},
		{"trivia ancient (user, once, 900k steps old)", rpc.ACLUser, 1, 0, now - 900000},
	}
	ages := []int64{100, 10000, 100000, 500000}
	for _, f := range facts {
		for _, age := range ages {
			r := Retrievability(now, now-age, 0)
			score := RankScore(f.hits, r, TrustWeight(f.tier), f.corr, RankBeta)
			fate := "KEEP"
			if score < probeEvictFloor {
				fate = "PRUNE"
			}
			t.Logf("%-48s age=%-7d score=%.6f fate=%s", f.name, age, score, fate)
		}
	}
	t.Logf("birth-anchored (age from each fact's own birth):")
	for _, f := range facts {
		age := now - f.bornAt
		r := Retrievability(now, f.bornAt, 0)
		score := RankScore(f.hits, r, TrustWeight(f.tier), f.corr, RankBeta)
		fate := "KEEP"
		if score < probeEvictFloor {
			fate = "PRUNE"
		}
		t.Logf("%-48s age=%-7d score=%.6f fate=%s", f.name, age, score, fate)
	}
}
