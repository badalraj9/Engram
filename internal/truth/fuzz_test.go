package truth

import (
	"testing"
)

// FuzzDecodeRecord (H6) proves the codec boundary never panics:
// arbitrary bytes decode to a validated record or a coded error —
// no slice-out-of-bounds, no hang. Seed corpus holds one framed
// garbage plus short/truncated shapes.
func FuzzDecodeRecord(f *testing.F) {
	f.Add([]byte{CodecVersion, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Add([]byte{9, 9, 9})
	f.Add([]byte{CodecVersion, 1, 2, 3, 4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, buf []byte) {
		rec, err := decodeRecord(buf)
		if err != nil {
			return
		}
		if err := rec.Validate(); err != nil {
			t.Fatalf("decoder returned an invalid record without error: %+v", err)
		}
	})
}

// FuzzDecodeFeedback applies the same never-panic rule to events.
func FuzzDecodeFeedback(f *testing.F) {
	f.Add([]byte{CodecVersion, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, buf []byte) {
		ev, err := decodeFeedback(buf)
		if err != nil {
			return
		}
		if err := ev.Validate(); err != nil {
			t.Fatalf("decoder returned an invalid event without error: %+v", err)
		}
	})
}
