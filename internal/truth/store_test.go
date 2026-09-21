package truth

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"engram/internal/rpc"
)

// goldenRecord is the pinned fixture for the golden-bytes test.
func goldenRecord() rpc.Record {
	return rpc.Record{
		ID:             "0123456789abcdef0123456789abcdef",
		Subject:        "alloy",
		Predicate:      "has-melting-point",
		Object:         "1600C",
		ValidTime:      rpc.Interval{From: 1000, To: rpc.OpenEnd},
		Txn:            rpc.Interval{From: 7, To: rpc.OpenEnd},
		Provenance:     rpc.Provenance{Source: "sim", Session: "s1"},
		DerivedFrom:    []string{"p1", "p2"},
		ACL:            rpc.ACLUser,
		Status:         rpc.StatusActive,
		Type:           rpc.RecordSummary,
		BaseConfidence: 0.8,
		CallerTSMeta:   99,
		Payload:        map[string]string{"raw": "excerpt"},
	}
}

// goldenHex is filled by TestPrintGolden (run once, paste, re-run).
const goldenHex = "01afaa382020303132333435363738396162636465663031323334353637383961626364656605616c6c6f79116861732d6d656c74696e672d706f696e7405313630304300000000000003e87fffffffffffffff00000000000000077fffffffffffffff0373696d027331020270310270320201023fe999999999999a000000000000006301037261770765786365727074"

func TestPrintGolden(t *testing.T) {
	if goldenHex != "PENDING" {
		t.Skip("golden pinned")
	}
	val, err := encodeRecord(goldenRecord())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("GOLDEN %s", hex.EncodeToString(val))
}

func TestCodecGolden(t *testing.T) {
	if goldenHex == "PENDING" {
		t.Skip("paste golden from TestPrintGolden first")
	}
	want, err := hex.DecodeString(goldenHex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeRecord(goldenRecord())
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != goldenHex {
		t.Fatalf("golden drift:\n got %s\nwant %s", hex.EncodeToString(got), goldenHex)
	}
	_ = want
	rec, err := decodeRecord(got)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Subject != "alloy" || rec.Type != rpc.RecordSummary || len(rec.DerivedFrom) != 2 {
		t.Fatalf("golden decode mismatch: %+v", rec)
	}
}

func TestCodecRejects(t *testing.T) {
	val, err := encodeRecord(goldenRecord())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]byte) []byte{
		"newer-version": func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[0] = CodecVersion + 1
			return c
		},
		"bad-crc": func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[len(c)-1] ^= 0xFF
			return c
		},
		"truncated": func(b []byte) []byte { return b[:len(b)-3] },
		"trailing":  func(b []byte) []byte { return append(append([]byte(nil), b...), 0x00) },
		"short":     func(b []byte) []byte { return b[:3] },
	}
	for name, mut := range cases {
		if _, err := decodeRecord(mut(val)); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	// Unknown enum byte: flip the ACL byte. Layout: version(1)+crc(4)+
	// id(uvarint+32)+sub+pred+obj+4xi64+src+sess+derived… — locate by decode
	// instead: craft via raw body edit is brittle, so assert policy only:
	if _, err := decodeACL(9); err == nil {
		t.Fatal("decodeACL(9): expected rejection")
	}
	if _, err := decodeStatus(9); err == nil {
		t.Fatal("decodeStatus(9): expected rejection")
	}
	if _, err := decodeType(9); err == nil {
		t.Fatal("decodeType(9): expected rejection")
	}
}

func testInput(sub, pred, obj string) rpc.InputRecord {
	return rpc.InputRecord{
		Subject:        sub,
		Predicate:      pred,
		Object:         obj,
		ValidTime:      rpc.Interval{From: 1000, To: rpc.OpenEnd},
		Provenance:     rpc.Provenance{Source: "sim", Session: "s1"},
		ACL:            rpc.ACLUser,
		BaseConfidence: 0.8,
		CallerTSMeta:   42,
		Payload:        map[string]string{"raw": "x"},
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAppendGet(t *testing.T) {
	s := openTest(t)
	rec, err := s.Append(testInput("alloy", "melts-at", "1600C"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" || rec.Status != rpc.StatusActive || rec.Type != rpc.RecordEpisodic {
		t.Fatalf("bad committed shape: %+v", rec)
	}
	if rec.Txn.From != 0 || !rec.Txn.Open() {
		t.Fatalf("txn not seq-rooted: %+v", rec.Txn)
	}
	got, err := s.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "alloy" || got.Txn.From != rec.Txn.From {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if _, err := s.Get("missing"); !isCode(err, rpc.CodeNotFound) {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestSupersedeAtomic(t *testing.T) {
	s := openTest(t)
	v1, err := s.Append(testInput("alloy", "melts-at", "1600C"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Supersede(v1.ID, testInput("alloy", "melts-at", "1700C"))
	if err != nil {
		t.Fatal(err)
	}
	if v2.Txn.From != 1 {
		t.Fatalf("expected seq 1, got %d", v2.Txn.From)
	}
	old, err := s.Get(v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != rpc.StatusTombstoned || old.Txn.To != 1 {
		t.Fatalf("old version not closed: %+v", old.Txn)
	}
	// Historical exact-key read at txn 0 sees v1 only.
	hist, err := s.QueryExact("alloy", "melts-at", 0, rpc.AxisTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Object != "1600C" {
		t.Fatalf("historical as_of broken: %+v", hist)
	}
	// Current read at txn 1 sees v2 only.
	cur, err := s.QueryExact("alloy", "melts-at", 1, rpc.AxisTransaction)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 1 || cur[0].Object != "1700C" {
		t.Fatalf("current as_of broken: %+v", cur)
	}
	if _, err := s.Supersede("missing", testInput("a", "b", "c")); !isCode(err, rpc.CodeNotFound) {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestRestatusBoth(t *testing.T) {
	s := openTest(t)
	v1, err := s.Append(testInput("alloy", "melts-at", "1600C"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Restatus(v1.ID, rpc.StatusBoth)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != rpc.StatusBoth || b.Subject != "alloy" {
		t.Fatalf("restatus content changed: %+v", b)
	}
	old, err := s.Get(v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != rpc.StatusTombstoned {
		t.Fatalf("old not tombstoned: %s", old.Status)
	}
	if _, err := s.Restatus(v1.ID, "WILD"); err == nil {
		t.Fatal("expected rejection of unknown status")
	}
}

func TestAppendDerived(t *testing.T) {
	s := openTest(t)
	e1, err := s.Append(testInput("a", "p", "1"))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := s.AppendDerived(testInput("topic", "summarizes", "a"), rpc.RecordSummary, []string{e1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Type != rpc.RecordSummary || len(sum.DerivedFrom) != 1 {
		t.Fatalf("bad derived shape: %+v", sum)
	}
	if _, err := s.AppendDerived(testInput("x", "y", "z"), rpc.RecordEpisodic, []string{e1.ID}); err == nil {
		t.Fatal("expected rejection of episodic on derived lane")
	}
	if _, err := s.AppendDerived(testInput("x", "y", "z"), rpc.RecordSummary, nil); err == nil {
		t.Fatal("expected rejection of parentless derived record")
	}
}

func TestFeedbackScanOrder(t *testing.T) {
	s := openTest(t)
	r1, err := s.Append(testInput("a", "p", "1"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Append(testInput("b", "q", "2"))
	if err != nil {
		t.Fatal(err)
	}
	ev := rpc.FeedbackEvent{RecordID: r1.ID, Kind: rpc.FeedbackAttestConfirm, Source: "u", Session: "s2", Tick: 4}
	if err := s.AppendFeedback(ev); err != nil {
		t.Fatal(err)
	}
	_ = r2
	var offsets []uint64
	var kinds []EventKind
	if err := s.Scan(0, func(e Event) error {
		offsets = append(offsets, e.Offset)
		kinds = append(kinds, e.Kind)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 3 || offsets[0] != 0 || offsets[1] != 1 || offsets[2] != 2 {
		t.Fatalf("scan order broken: %v", offsets)
	}
	if kinds[2] != EventFeedback {
		t.Fatalf("feedback not third: %v", kinds)
	}
	// Resume-from-offset.
	var resumed []uint64
	if err := s.Scan(2, func(e Event) error {
		resumed = append(resumed, e.Offset)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0] != 2 {
		t.Fatalf("resume broken: %v", resumed)
	}
}

func TestCorruptTailAutoTruncate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		r, err := s.Append(testInput("k", "p", fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	// Corrupt the final value in place (simulated torn tail write).
	err = s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(2))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			corrupt := append([]byte(nil), val...)
			corrupt[len(corrupt)-1] ^= 0xFF
			return txn.Set(recordKey(2), corrupt)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	corruptID := ids[2]
	s.Close()

	// Reopen: pure-tail corruption auto-amputates, loudly.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	truncated, at := s2.Truncated()
	if !truncated || at != 2 {
		t.Fatalf("expected auto-truncate at 2, got %v at %d", truncated, at)
	}
	var count int
	if err := s2.Scan(0, func(e Event) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 surviving events, got %d", count)
	}
	tail, _ := s2.TailOffset()
	if tail != 2 {
		t.Fatalf("expected tail 2, got %d", tail)
	}
	// Absence, never partial data.
	if _, err := s2.Get(corruptID); !isCode(err, rpc.CodeNotFound) {
		t.Fatalf("expected NOT_FOUND for amputated record, got %v", err)
	}
	// Store writable at the truncated tail.
	r, err := s2.Append(testInput("k", "p", "new"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Txn.From != 2 {
		t.Fatalf("expected reuse of seq 2, got %d", r.Txn.From)
	}
}

func TestMidLogRefusesOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Append(testInput("k", "p", fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	// Corrupt a middle value with intact successors.
	err = s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(1))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			corrupt := append([]byte(nil), val...)
			corrupt[len(corrupt)-1] ^= 0xFF
			return txn.Set(recordKey(1), corrupt)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Open must refuse: amputating would silently delete the good suffix.
	_, err = Open(dir)
	if err == nil || !isCode(err, rpc.CodeCorruptIndex) || !strings.Contains(err.Error(), "seq 1") {
		t.Fatalf("expected loud CORRUPT refusal at seq 1, got %v", err)
	}
}

func TestKeyUniqueness(t *testing.T) {
	s := openTest(t)
	var ids []string
	for i := 0; i < 200; i++ {
		r, err := s.Append(testInput("k", "p", fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	for i := 0; i < 50; i++ {
		if _, err := s.Supersede(ids[i], testInput("k", "p", fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[uint64]bool)
	var total int
	if err := s.Scan(0, func(e Event) error {
		if seen[e.Offset] {
			return fmt.Errorf("duplicate offset %d", e.Offset)
		}
		seen[e.Offset] = true
		total++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if total != 250 {
		t.Fatalf("expected 250 events, got %d", total)
	}
}

// TestM8aKill9 spawns a writer child, SIGKILLs it mid-stream, and
// verifies the surviving prefix replays bit-identical to a clean run.
func TestM8aKill9(t *testing.T) {
	if os.Getenv("ENGRAM_M8A_CHILD") == "1" {
		m8aChild(t)
		return
	}
	dir := t.TempDir()
	bin := os.Args[0]
	cmd := exec.Command(bin, "-test.run=TestM8aKill9", "-test.v")
	cmd.Env = append(os.Environ(), "ENGRAM_M8A_CHILD=1", "ENGRAM_M8A_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait for 3 progress signals (~150 records), then kill mid-stream.
	signals := 0
	buf := make([]byte, 64)
	for signals < 3 {
		n, rerr := out.Read(buf)
		if rerr != nil || n == 0 {
			break
		}
		signals += strings.Count(string(buf[:n]), "\n")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	// Reopen: every surviving entry must checksum-clean, store usable.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var recs []rpc.Record
	scanErr := s.Scan(0, func(e Event) error {
		if e.Kind == EventRecord {
			recs = append(recs, e.Record)
		}
		return nil
	})
	// Either a clean prefix, or a loud truncation at the exact tail.
	if scanErr != nil {
		t.Logf("loud truncation (acceptable): %v", scanErr)
	}
	if len(recs) == 0 {
		t.Fatal("no surviving records after kill")
	}
	// Deterministic content, all fields except random ID: encoding is
	// deterministic, so content-equality plus the pinned golden codec
	// closes the chain to byte-identity.
	for i, r := range recs {
		want := expectM8a(i)
		want.ID = r.ID
		if !reflect.DeepEqual(want, r) {
			t.Fatalf("prefix not bit-identical at %d:\n got %+v\nwant %+v", i, r, want)
		}
	}
	if _, err := s.Append(testInput("m8a", "p", "post-crash")); err != nil {
		t.Fatal(err)
	}
}

func expectM8a(i int) rpc.Record {
	return rpc.Record{
		Subject:        "m8a",
		Predicate:      "p",
		Object:         fmt.Sprint(i),
		ValidTime:      rpc.Interval{From: 1000, To: rpc.OpenEnd},
		Txn:            rpc.Interval{From: int64(i), To: rpc.OpenEnd},
		Provenance:     rpc.Provenance{Source: "sim", Session: "s1"},
		ACL:            rpc.ACLUser,
		Status:         rpc.StatusActive,
		Type:           rpc.RecordEpisodic,
		BaseConfidence: 0.8,
		CallerTSMeta:   42,
		Payload:        map[string]string{"raw": "x"},
	}
}

func m8aChild(t *testing.T) {
	dir := os.Getenv("ENGRAM_M8A_DIR")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 1000; i++ {
		in := testInput("m8a", "p", fmt.Sprint(i))
		if _, err := s.Append(in); err != nil {
			t.Fatal(err)
		}
		if i%10 == 9 {
			if err := s.Sync(); err != nil {
				t.Fatal(err)
			}
		}
		if i%50 == 49 {
			fmt.Println("tick", i)
		}
	}
}

func TestMultiOffsetCuts(t *testing.T) {
	val, err := encodeRecord(goldenRecord())
	if err != nil {
		t.Fatal(err)
	}
	offsets := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
	for off := 7; off < len(val); off += 7 {
		offsets[off] = true
	}
	offsets[len(val)-1] = true
	for off := range offsets {
		if _, err := decodeRecord(val[:off]); err == nil {
			t.Fatalf("cut at %d decoded cleanly", off)
		}
	}
}

// TestSupersedeCrashXOR kills a writer mid-supersede-loop and asserts
// whole-entry atomicity: for every supersede, exactly one side holds —
// (old closed AND new present) XOR (old open AND new absent). Chain
// continuity (prev.txn_to == next.txn_from, closed ones TOMBSTONED)
// makes halves unrepresentable in the assertion itself.
func TestSupersedeCrashXOR(t *testing.T) {
	if os.Getenv("ENGRAM_XOR_CHILD") == "1" {
		xorChild(t)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestSupersedeCrashXOR")
	cmd.Env = append(os.Environ(), "ENGRAM_XOR_CHILD=1", "ENGRAM_XOR_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	signals := 0
	buf := make([]byte, 64)
	for signals < 2 {
		n, rerr := out.Read(buf)
		if rerr != nil || n == 0 {
			break
		}
		signals += strings.Count(string(buf[:n]), "\n")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var versions []rpc.Record
	if err := s.Scan(0, func(e Event) error {
		if e.Kind == EventRecord && e.Record.Subject == "xor" {
			versions = append(versions, e.Record)
		}
		return nil
	}); err != nil {
		t.Fatalf("scan must be clean (atomic txns leave no halves): %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("no surviving versions")
	}
	open := 0
	for i, v := range versions {
		if v.Txn.Open() {
			open++
		}
		if i > 0 {
			prev := versions[i-1]
			if v.Txn.From != prev.Txn.To {
				t.Fatalf("chain break between %d and %d", prev.Txn.From, v.Txn.From)
			}
			if prev.Status != rpc.StatusTombstoned {
				t.Fatalf("closed version not tombstoned: %+v", prev)
			}
		}
	}
	if open != 1 {
		t.Fatalf("expected exactly one open version, got %d", open)
	}
}

func xorChild(t *testing.T) {
	s, err := Open(os.Getenv("ENGRAM_XOR_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head, err := s.Append(testInput("xor", "v", "0"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 500; i++ {
		head, err = s.Supersede(head.ID, testInput("xor", "v", fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		if i%10 == 9 {
			if err := s.Sync(); err != nil {
				t.Fatal(err)
			}
		}
		if i%50 == 49 {
			fmt.Println("tick", i)
		}
	}
}

// TestAckContract proves the ack rule means something before P5 exists:
// Append returns, Sync returns, process dies, the record is present.
func TestAckContract(t *testing.T) {
	if os.Getenv("ENGRAM_ACK_CHILD") == "1" {
		ackChild(t)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestAckContract")
	cmd.Env = append(os.Environ(), "ENGRAM_ACK_CHILD=1", "ENGRAM_ACK_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, rerr := out.Read(buf)
	if rerr != nil || n == 0 {
		t.Fatalf("no ready signal: %v", rerr)
	}
	id := strings.TrimSpace(strings.TrimPrefix(string(buf[:n]), "ready "))
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(strings.TrimSpace(id))
	if err != nil {
		t.Fatalf("synced record missing after kill: %v", err)
	}
	if got.Subject != "ack" || got.Object != "v" || got.Txn.From != 0 {
		t.Fatalf("wrong record: %+v", got)
	}
}

func ackChild(t *testing.T) {
	s, err := Open(os.Getenv("ENGRAM_ACK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Append(testInput("ack", "p", "v"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready " + r.ID)
	time.Sleep(30 * time.Second)
}

func isCode(err error, c rpc.Code) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), string(c))
}

func TestIfaceConformance(t *testing.T) {
	var _ TruthStore = (*Store)(nil)
}
