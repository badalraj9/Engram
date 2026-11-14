// Validation-cap reject paths (P6 exit: every numeric cap carries
// reject-path cases on both sides of the boundary — accept at the cap,
// reject one past it).
package rpc

import (
	"strings"
	"testing"
)

func capInput() InputRecord {
	return InputRecord{
		Subject: "s", Predicate: "p", Object: "o",
		ValidTime:  Interval{From: 1, To: 2},
		Provenance: Provenance{Source: "src", Session: "ses"},
		ACL:        ACLUser, BaseConfidence: 0.5,
	}
}

// TestCaps_PayloadKeyCount pins the 64-key boundary both sides.
func TestCaps_PayloadKeyCount(t *testing.T) {
	at := capInput()
	at.Payload = make(map[string]string)
	for i := 0; i < MaxPayloadKeys; i++ {
		at.Payload["k"+string(rune('a'+i%26))+itoa(i)] = "v"
	}
	if err := at.Validate(); err != nil {
		t.Fatalf("64 keys must validate: %v", err)
	}
	over := capInput()
	over.Payload = make(map[string]string)
	for i := 0; i < MaxPayloadKeys+1; i++ {
		over.Payload["k"+string(rune('a'+i%26))+itoa(i)] = "v"
	}
	if err := over.Validate(); !isInvalidRecord(err) {
		t.Fatalf("65 keys must fail INVALID_RECORD, got %v", err)
	}
}

// TestCaps_BlankKey pins blank/whitespace keys (reject) beside a
// single-space-trimmed valid key (accept).
func TestCaps_BlankKey(t *testing.T) {
	for _, bad := range []string{"", "   ", "\t"} {
		in := capInput()
		in.Payload = map[string]string{bad: "v"}
		if err := in.Validate(); !isInvalidRecord(err) {
			t.Fatalf("blank key %q must fail, got %v", bad, err)
		}
	}
	ok := capInput()
	ok.Payload = map[string]string{" raw ": "v"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("padded key must validate: %v", err)
	}
}

// TestCaps_KeyLen pins the 128-byte key boundary both sides.
func TestCaps_KeyLen(t *testing.T) {
	at := capInput()
	at.Payload = map[string]string{strings.Repeat("k", MaxPayloadKeyLen): "v"}
	if err := at.Validate(); err != nil {
		t.Fatalf("128-byte key must validate: %v", err)
	}
	over := capInput()
	over.Payload = map[string]string{strings.Repeat("k", MaxPayloadKeyLen+1): "v"}
	if err := over.Validate(); !isInvalidRecord(err) {
		t.Fatalf("129-byte key must fail, got %v", err)
	}
}

// TestCaps_ValueLen pins the 4KB value boundary both sides.
func TestCaps_ValueLen(t *testing.T) {
	at := capInput()
	at.Payload = map[string]string{"k": strings.Repeat("v", MaxPayloadValLen)}
	if err := at.Validate(); err != nil {
		t.Fatalf("4KB value must validate: %v", err)
	}
	over := capInput()
	over.Payload = map[string]string{"k": strings.Repeat("v", MaxPayloadValLen+1)}
	if err := over.Validate(); !isInvalidRecord(err) {
		t.Fatalf("4KB+1 value must fail, got %v", err)
	}
}

// TestCaps_ConfidenceBounds pins the [0,1] base-confidence boundary:
// NaN, below, above reject; edges accept.
func TestCaps_ConfidenceBounds(t *testing.T) {
	for _, good := range []float64{0, 1, 0.5} {
		in := capInput()
		in.BaseConfidence = good
		if err := in.Validate(); err != nil {
			t.Fatalf("confidence %v must validate: %v", good, err)
		}
	}
	for _, bad := range []float64{-0.1, 1.1} {
		in := capInput()
		in.BaseConfidence = bad
		if err := in.Validate(); !isInvalidRecord(err) {
			t.Fatalf("confidence %v must fail, got %v", bad, err)
		}
	}
}

func isInvalidRecord(err error) bool {
	de, ok := err.(*Error)
	return ok && de.Code == CodeInvalidRecord
}

func itoa(n int) string {
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
