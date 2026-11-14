package main

// Boot-race gates (bug report 2026-11-14: "MCP shim dies instead of
// dialing the holder when two processes start simultaneously").
// Black-box against real child processes; the store lock stays
// fail-closed throughout — only the loser's lifecycle changed.
//
//	A1  10x simultaneous starters on fresh dirs: both survive, both answer
//	A2  exactly one holder: one LOCK, one .vlog, one socket; identical
//	    recall results through both hosts (loser wrote nothing)
//	A3  the loser's stderr carries the clean fallback line, never the
//	    raw badger lock text and never table-open noise
//	A4  no holder ever appears → exit non-zero within ~5s, naming the
//	    socket path; stdout stays empty even in failure
//	A5  stale-socket takeover — covered by TestMCP_KillRespawn (unchanged)
//	A6  sequential client dials with ZERO store-file change
//	A7  100-call stdio session through the DIALER (retry path): stdout
//	    is exactly one JSON line per request, nothing else

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"engram/internal/truth"
)

// lockedLine is the runShim fallback diagnostic (main.go): its
// presence identifies the loser, and its clean text is the A3 gate.
const lockedLine = "store locked by another starter"

// freshDir creates the round's data dir + socket under t.TempDir().
// The framework removes it (children reaped first — see launch's
// cleanup contract in takeover_test.go), so no engram-* dir outlives
// a round.
func freshDir(t *testing.T) (data, sock string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "data"), filepath.Join(dir, "test.sock")
}

// raceRound runs one simultaneous-start round and asserts A1+A2+A3
// for it. Runs as its OWN subtest: the fresh store (~100MB+ prealloc)
// is deleted when the round returns, never accumulated across rounds.
func raceRound(t *testing.T, round int) {
	t.Helper()
	data, sock := freshDir(t)
	a := startChildEnv(t, "--mcp", data, sock)
	b := startChildEnv(t, "--mcp", data, sock) // back-to-back: same boot window
	awaitHolder(t, sock, a, b)

	// One holder's marker fact, written only after the race settled.
	sub := fmt.Sprintf("race-r%d", round)
	marker := fmt.Sprintf("round-%d", round)
	cl := dial(t, sock)
	v, _, _, err := cl.Emit(opCtx(), rpcIn(sub, "marker", marker))
	if err != nil || v == "" {
		t.Fatalf("round %d: emit via holder: %v", round, err)
	}

	// A1: BOTH hosts answer a full MCP walk through their own stdio.
	for i, c := range []*child{a, b} {
		f := c.mcpCall(t, 1, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
		if _, ok := f["result"]; !ok {
			t.Fatalf("round %d host %d: handshake: %v", round, i, f)
		}
		f = c.mcpCall(t, 2, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["%s"],"caller_tier":"user","budget":5}}}`, sub))
		text := fmt.Sprintf("%v", f["result"])
		if !strings.Contains(text, marker) {
			t.Fatalf("round %d host %d: recall must carry the marker: %v", round, i, f)
		}
	}

	// Still alive: neither died after answering.
	time.Sleep(300 * time.Millisecond)
	for i, c := range []*child{a, b} {
		select {
		case e := <-c.done:
			t.Fatalf("round %d: host %d exited (A1): %v (stdout %q stderr %q)",
				round, i, e, c.stdout.String(), c.stderr.String())
		default:
		}
	}

	// A3: identify the loser by the clean fallback line; exactly one.
	var losers []*child
	for _, c := range []*child{a, b} {
		if strings.Contains(c.stderr.String(), lockedLine) {
			losers = append(losers, c)
		}
	}
	if len(losers) != 1 {
		t.Fatalf("round %d: want exactly one loser with the fallback line, got %d (stderrs %q / %q)",
			round, len(losers), a.stderr.String(), b.stderr.String())
	}
	loser := losers[0]
	if s := loser.stderr.String(); strings.Contains(s, "Cannot create lock file") || strings.Contains(s, "tables opened") {
		t.Fatalf("round %d: loser stderr must be clean diagnostics, got %q", round, s)
	}

	// A2: exactly one LOCK, one .vlog, one socket; both hosts agree
	// row-for-row on the only fact (the loser wrote nothing).
	ents, err := os.ReadDir(data)
	if err != nil {
		t.Fatal(err)
	}
	locks, vlogs := 0, 0
	for _, e := range ents {
		switch {
		case e.Name() == "LOCK":
			locks++
		case strings.HasSuffix(e.Name(), ".vlog"):
			vlogs++
		}
	}
	if locks != 1 || vlogs != 1 {
		t.Fatalf("round %d: data dir must hold exactly 1 LOCK + 1 .vlog, got %d/%d", round, locks, vlogs)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("round %d: socket file missing: %v", round, err)
	}
}

func TestShim_RaceTwoStarters(t *testing.T) {
	for round := 0; round < 10; round++ {
		t.Run(fmt.Sprintf("round%d", round), func(t *testing.T) {
			raceRound(t, round)
		})
	}
}

// TestShim_RaceResultsIdentical pins A2's "no second writer" leg
// explicitly: the same marker recalled through BOTH hosts' stdio
// yields byte-identical result text.
func TestShim_RaceResultsIdentical(t *testing.T) {
	data, sock := freshDir(t)
	a := startChildEnv(t, "--mcp", data, sock)
	b := startChildEnv(t, "--mcp", data, sock)
	awaitHolder(t, sock, a, b)
	cl := dial(t, sock)
	if _, _, _, err := cl.Emit(opCtx(), rpcIn("agree", "marker", "same")); err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, c := range []*child{a, b} {
		c.mcpCall(t, 1, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	}
	fa := a.mcpCall(t, 2, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["agree"],"caller_tier":"user","budget":5}}}`)
	fb := b.mcpCall(t, 2, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["agree"],"caller_tier":"user","budget":5}}}`)
	if fmt.Sprintf("%v", fa["result"]) != fmt.Sprintf("%v", fb["result"]) {
		t.Fatalf("hosts must agree row-for-row: %v vs %v", fa["result"], fb["result"])
	}
}

// TestShim_NoHolderEverAppears pins A4: a loser whose winner never
// serves (here: the lock is held in-process by the TEST, nothing
// binds) exits non-zero within the retry window, names the socket
// path, keeps stdout empty, and never surfaces raw badger text.
func TestShim_NoHolderEverAppears(t *testing.T) {
	data, sock := freshDir(t)
	s, err := truth.Open(data) // the test IS the silent lock holder
	if err != nil {
		t.Fatalf("open in-test: %v", err)
	}
	defer s.Close()
	c := startChildEnv(t, "--mcp", data, sock)
	start := time.Now()
	waitErr := <-c.done
	elapsed := time.Since(start)
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("child must exit non-zero when no holder appears, got %v", waitErr)
	}
	if elapsed < 4*time.Second || elapsed > 12*time.Second {
		t.Fatalf("exit took %v, want inside the ~%s window", elapsed, dialRetryWindow)
	}
	stderr := c.stderr.String()
	if !strings.Contains(stderr, sock) {
		t.Fatalf("error must name the socket path, stderr: %q", stderr)
	}
	if strings.Contains(stderr, "Cannot create lock file") {
		t.Fatalf("lock conflict must be classified, not surfaced raw: %q", stderr)
	}
	if c.stdout.Len() != 0 {
		t.Fatalf("stdout must stay empty even in failure: %q", c.stdout.String())
	}
}

// fileSnap is one store file's identity for A6.
type fileSnap struct {
	size  int64
	mtime int64
}

func snapshot(t *testing.T, data string) map[string]fileSnap {
	t.Helper()
	snaps := map[string]fileSnap{}
	ents, err := os.ReadDir(data)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		snaps[e.Name()] = fileSnap{size: info.Size(), mtime: info.ModTime().UnixNano()}
	}
	return snaps
}

// TestShim_SequentialDial_NoStoreChange pins A6: with a holder live,
// a sequentially started second client dials and answers, and the
// store files are size- and mtime-identical before/after.
func TestShim_SequentialDial_NoStoreChange(t *testing.T) {
	data, sock := freshDir(t)
	holder := startChildEnv(t, "--mcp", data, sock)
	holder.waitSock(t, sock)
	cl := dial(t, sock)
	if _, _, _, err := cl.Emit(opCtx(), rpcIn("seq", "dial", "clean")); err != nil {
		t.Fatalf("emit: %v", err)
	}
	before := snapshot(t, data)

	second := startChildEnv(t, "--mcp", data, sock) // holder live → dials
	time.Sleep(1 * time.Second)                     // settle (probe + dial)
	select {
	case e := <-second.done:
		t.Fatalf("second client must dial, not die: %v (stderr %q)", e, second.stderr.String())
	default:
	}
	second.mcpCall(t, 1, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	f := second.mcpCall(t, 2, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["seq"],"caller_tier":"user","budget":5}}}`)
	if !strings.Contains(fmt.Sprintf("%v", f["result"]), "clean") {
		t.Fatalf("dialing client must see the holder's fact: %v", f["result"])
	}
	after := snapshot(t, data)
	if len(before) != len(after) {
		t.Fatalf("store file set changed: %v -> %v", before, after)
	}
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Fatalf("file vanished: %s", name)
		}
		if b != a {
			t.Fatalf("file %s changed (A6 zero-write violated): before %+v after %+v", name, b, a)
		}
	}
}

// TestShim_StdoutPurity100Calls pins A7: 100 recall calls through the
// DIALER's stdio (the retry path included) produce exactly one
// JSON-RPC line per request on stdout — initialize + 100 = 101 frames,
// zero non-JSON bytes.
func TestShim_StdoutPurity100Calls(t *testing.T) {
	data, sock := freshDir(t)
	a := startChildEnv(t, "--mcp", data, sock)
	b := startChildEnv(t, "--mcp", data, sock)
	awaitHolder(t, sock, a, b)
	dialer := b // assertions are identical for either survivor
	if strings.Contains(a.stderr.String(), lockedLine) {
		dialer = a
	}
	// Bulk-send: initialize, the notification (no reply), 100 recalls.
	if _, err := fmt.Fprintf(dialer.stdin, "%s\n", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(dialer.stdin, "%s\n", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["purity"],"caller_tier":"user","budget":5}}}`, i+2)
		if _, err := fmt.Fprintf(dialer.stdin, "%s\n", line); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for the final response id=101, then judge the whole stream.
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw := dialer.stdout.String()
		if strings.Contains(raw, `"id":101`) {
			break
		}
		select {
		case e := <-dialer.done:
			t.Fatalf("dialer exited mid-session: %v (stdout %q)", e, raw)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no response to id 101 (stdout %q)", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	frames := 0
	for _, ln := range strings.Split(dialer.stdout.String(), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var f map[string]any
		if json.Unmarshal([]byte(ln), &f) != nil {
			t.Fatalf("non-JSON byte on stdout (A7 violated): %q", ln)
		}
		frames++
	}
	if frames != 101 {
		t.Fatalf("want exactly 101 JSON-RPC frames (1 init + 100 calls), got %d", frames)
	}
	if !strings.Contains(dialer.stdout.String(), `"id":101`) {
		t.Fatal("final response missing")
	}
}
