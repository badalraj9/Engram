// Operator repair (P5 — DESIGN §8, BUILD-PLAN P5 exit).
//
// Mid-log corruption with intact successors refuses open fail-closed;
// only an explicit operator action may amputate history. This file is
// that action: a loudly-audited wrapper over the unexported
// truncateFrom primitive (store.go), which names recovery and this
// path as its only two callers.
//
// Gating: operator must be true (the server sets it only for a
// local-operator channel, never for the 7 RPC ops) and reason
// non-empty for the audit line. Anything else fails closed without
// touching the log. Every invocation logs to stderr BEFORE mutating,
// so the audit line survives even a mid-repair crash.
//
// Additive concrete method on *Store only — the TruthStore interface
// is P0-closed and stays untouched.
package truth

import (
	"os"
	"strings"

	"engram/internal/rpc"
)

// RepairTruncateFrom amputates every versioned key at sequence >= seq
// and rebuilds the in-memory index. Returns the key count removed.
// Operator-gated: operator=false or blank reason fails closed with no
// mutation. Loud: one stderr line per invocation, before truncating.
func (s *Store) RepairTruncateFrom(seq uint64, reason string, operator bool) (uint64, error) {
	if !operator || strings.TrimSpace(reason) == "" {
		return 0, rpc.E(rpc.CodeInvalidRecord, "truth: repair requires operator authorization and a reason")
	}
	tail, err := s.TailOffset()
	if err != nil {
		return 0, err
	}
	if seq > tail {
		return 0, rpc.E(rpc.CodeInvalidRecord, "truth: repair seq beyond log tail")
	}
	_, _ = os.Stderr.WriteString("engram REPAIR truncate-from=" + itoa(seq) + " tail=" + itoa(tail) + " reason=" + strings.TrimSpace(reason) + "\n")
	return s.truncateFrom(seq)
}

// itoa formats a uint64 without importing strconv into the hot path
// file — repair is cold, but the file keeps stdlib-minimal shape.
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
