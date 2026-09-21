// Key layout (TECH-STACK §2.5, P1 locked).
//
// Primary key = logical prefix (1 byte) + commit sequence (uint64,
// big-endian). Every version is a distinct key, so compaction can
// never merge multi-version history — by construction, not by flag.
// The sequence namespace is global across all prefixes (one clock).
//
// Secondaries are versioned: prefix + length-prefixed components +
// sequence. Length-prefix framing (uvarint) keeps ("a","bc") distinct
// from ("ab","c"). Latest lookup is a reverse-seek on the secondary
// prefix; historical lookup a prefix scan + interval filter.
package truth

import (
	"encoding/binary"
	"fmt"

	"engram/internal/rpc"
)

// Key prefixes. R/F carry the commit sequence; M holds the schema
// marker (no sequence); S/O carry framed components + sequence.
const (
	prefixRecord   byte = 'R'
	prefixFeedback byte = 'F'
	prefixMeta     byte = 'M'
	prefixSubPred  byte = 'S'
	prefixObjPred  byte = 'O'
)

// schemaKeyName identifies the codec-version marker under prefixMeta.
const schemaKeyName = "schema"

// CodecVersion is the value-encoding version. Readers reject newer
// versions fail-closed; the golden-bytes test pins the layout beneath.
const CodecVersion byte = 1

// schemaKey is the M/schema marker key.
func schemaKey() []byte {
	k := []byte{prefixMeta}
	return append(k, schemaKeyName...)
}

// recordKey builds the primary key for a record version.
func recordKey(seq uint64) []byte {
	k := make([]byte, 0, 9)
	k = append(k, prefixRecord)
	return binary.BigEndian.AppendUint64(k, seq)
}

// feedbackKey builds the primary key for a feedback event.
func feedbackKey(seq uint64) []byte {
	k := make([]byte, 0, 9)
	k = append(k, prefixFeedback)
	return binary.BigEndian.AppendUint64(k, seq)
}

// frameComponent appends a length-prefixed string component.
func frameComponent(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// secondarySPKey builds (subject,predicate)+seq under prefixSubPred.
func secondarySPKey(sub, pred string, seq uint64) []byte {
	k := make([]byte, 0, 32)
	k = append(k, prefixSubPred)
	k = frameComponent(k, sub)
	k = frameComponent(k, pred)
	return binary.BigEndian.AppendUint64(k, seq)
}

// secondaryOPKey builds (object,predicate)+seq under prefixObjPred.
func secondaryOPKey(obj, pred string, seq uint64) []byte {
	k := make([]byte, 0, 32)
	k = append(k, prefixObjPred)
	k = frameComponent(k, obj)
	k = frameComponent(k, pred)
	return binary.BigEndian.AppendUint64(k, seq)
}

// secondarySPPrefix is the lookup prefix for one (subject,predicate).
func secondarySPPrefix(sub, pred string) []byte {
	k := make([]byte, 0, 32)
	k = append(k, prefixSubPred)
	k = frameComponent(k, sub)
	k = frameComponent(k, pred)
	return k
}

// secondaryOPPrefix is the lookup prefix for one (object,predicate).
func secondaryOPPrefix(obj, pred string) []byte {
	k := make([]byte, 0, 32)
	k = append(k, prefixObjPred)
	k = frameComponent(k, obj)
	k = frameComponent(k, pred)
	return k
}

// parseSeqFromKey extracts the trailing big-endian sequence.
// Primary keys are exactly 9 bytes; secondary keys end in 8 seq bytes
// after framed components (minimal structural check only).
func parseSeqFromKey(key []byte) (uint64, error) {
	if len(key) < 9 {
		return 0, rpc.E(rpc.CodeCorruptIndex, fmt.Sprintf("short key (%d bytes)", len(key)))
	}
	return binary.BigEndian.Uint64(key[len(key)-8:]), nil
}

// keyPrefix returns the single-byte prefix namespace of key.
func keyPrefix(key []byte) byte {
	if len(key) == 0 {
		return 0
	}
	return key[0]
}
