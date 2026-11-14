package main

// R0 takeover gates (BUILD-PLAN R0 item 1), black-box against real
// child processes (kill tests use Process.Kill per AGENTS §7 —
// never a clean close):
//
//   - becomes-holder: --mcp with no holder opens the store, serves
//     MCP AND the socket, keeps stdout at ZERO bytes, releases the
//     lock cleanly on EOF
//   - two-host race: the first process holds, the second connects —
//     both work, one writer, no corruption
//   - kill-respawn: holder killed mid-session → next spawn recovers
//     the store with the acked data (zero manual steps)

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	goClient "engram/client/go"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// exe builds the test binary once for all child-process tests.
var (
	exeOnce sync.Once
	exePath string
	exeDir  string
	exeErr  error
)

// TestMain guarantees the suite's disk footprint: the shared exe dir
// is removed after the run, and any NEW engram-* dir left in the
// system temp (BUILD-PLAN guard rail: a fresh store costs ~100MB+
// prealloc) fails the run loudly — no leak may outlive this suite.
func TestMain(m *testing.M) {
	before := tempEngramDirs()
	code := m.Run()
	if exeDir != "" {
		_ = os.RemoveAll(exeDir)
	}
	leaked := tempEngramDirs()
	for d := range before {
		delete(leaked, d)
	}
	if len(leaked) > 0 {
		names := make([]string, 0, len(leaked))
		for d := range leaked {
			names = append(names, d)
		}
		fmt.Fprintf(os.Stderr, "engramd tests leaked temp dirs: %v\n", names)
		code = 1
	}
	os.Exit(code)
}

func tempEngramDirs() map[string]bool {
	found := map[string]bool{}
	ents, _ := os.ReadDir(os.TempDir())
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "engram-") {
			found[filepath.Join(os.TempDir(), e.Name())] = true
		}
	}
	return found
}

func exe(t *testing.T) string {
	t.Helper()
	exeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "engramd-takeover")
		if err != nil {
			exeErr = err
			return
		}
		exeDir = dir
		exePath = filepath.Join(dir, "engramd-under-test.exe")
		build := exec.Command("go", "build", "-o", exePath, ".")
		if out, err := build.CombinedOutput(); err != nil {
			exeErr = fmt.Errorf("build: %v: %s", err, out)
		}
	})
	if exeErr != nil {
		t.Fatalf("exe: %v", exeErr)
	}
	return exePath
}

// child is one engramd-under-test process with captured stdout and
// stderr (zero-byte / clean-stderr assertions) and a held-open stdin
// (the MCP session).
type child struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *strings.Builder
	stderr *strings.Builder
	done   chan error
	exited chan struct{}
}

// launch starts mode with extra env pins layered over os.Environ().
// Cleanup kills the child and WAITS for the reaper before returning —
// on Windows the store dir's file locks release only on process
// reaping, so the LIFO-registered t.TempDir() removal (the round's
// fresh store, ~100MB+ prealloc) must never run while a child lives.
func launch(t *testing.T, mode string, extraEnv ...string) *child {
	t.Helper()
	c := &child{
		stdout: &strings.Builder{},
		stderr: &strings.Builder{},
		done:   make(chan error, 1),
		exited: make(chan struct{}),
	}
	argv := []string{mode}
	if mode == "" { // bare serve: no flag argument at all
		argv = nil
	}
	cmd := exec.Command(exe(t), argv...)
	cmd.Env = append(os.Environ(), extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	c.cmd = cmd
	c.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(c.stdout, out) }()
	go func() { _, _ = io.Copy(c.stderr, errPipe) }()
	go func() {
		err := cmd.Wait()
		c.done <- err
		close(c.exited)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = c.cmd.Process.Kill()
		select {
		case <-c.exited:
		case <-time.After(5 * time.Second):
		}
	})
	return c
}

// startChildEnv launches mode with explicit ENGRAM_* pins — the env
// shape the host configs carry (both vars pinned, socket explicit).
// Only safe to call from the test goroutine (uses t.Fatal).
func startChildEnv(t *testing.T, mode, data, sock string) *child {
	t.Helper()
	return launch(t, mode, "ENGRAM_DATA_DIR="+data, "ENGRAM_UDS_PATH="+sock)
}

// startChildData launches mode with ONLY ENGRAM_DATA_DIR pinned — the
// STEP 0 shape: the data dir derives the socket.
func startChildData(t *testing.T, mode, data string) *child {
	t.Helper()
	return launch(t, mode, "ENGRAM_DATA_DIR="+data)
}

func (c *child) waitSock(t *testing.T, sock string) {
	t.Helper()
	awaitHolder(t, sock, c)
}

// awaitHolder polls until SOME holder answers on sock — not tied to
// one child (the boot race decides who binds). Any listed child that
// exits early fails the wait with its captured streams.
func awaitHolder(t *testing.T, sock string, kids ...*child) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if c, err := goClient.Dial(sock); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _, rerr := c.Recall(ctx, []string{"engram-holder-probe"}, 1, "system")
			cancel()
			_ = c.Close()
			if rerr == nil {
				return
			}
		}
		for _, k := range kids {
			select {
			case e := <-k.done:
				t.Fatalf("child died before serving: %v (stdout %q stderr %q)",
					e, k.stdout.String(), k.stderr.String())
			default:
			}
		}
		if time.Now().After(deadline) {
			stderrs := make([]string, len(kids))
			for i, k := range kids {
				stderrs[i] = k.stderr.String()
			}
			t.Fatalf("socket never answered (stderrs: %q)", stderrs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// mcpCall drives one MCP round through the child's stdin and waits
// for the JSON-RPC response frame with that id on its stdout.
func (c *child) mcpCall(t *testing.T, id int, line string) map[string]any {
	t.Helper()
	if _, err := io.WriteString(c.stdin, line+"\n"); err != nil {
		t.Fatalf("mcpCall write: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, ln := range strings.Split(c.stdout.String(), "\n") {
			ln = strings.TrimSpace(ln)
			if !strings.HasPrefix(ln, "{") {
				continue
			}
			var f map[string]any
			if json.Unmarshal([]byte(ln), &f) != nil {
				t.Fatalf("stdout is not JSON (purity broken): %q", ln)
			}
			if f["id"] == float64(id) {
				return f
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no response to id %d (stdout %q)", id, c.stdout.String())
	return nil
}

func dial(t *testing.T, sock string) *goClient.Client {
	t.Helper()
	c, err := goClient.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func opCtx() context.Context {
	// Local socket, short ops; hang-protection lives in the dial
	// transport and waitSock, not here.
	return context.Background()
}

func rpcIn(sub, pred, obj string) rpc.InputRecord {
	return rpc.InputRecord{
		Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 0, To: rpc.OpenEnd},
		Provenance: rpc.Provenance{Source: "t", Session: "t1"},
		ACL:        rpc.ACLUser,
	}
}

// TestMCP_BecomesHolder: no holder running → the --mcp process opens
// the store, serves MCP and the socket, keeps stdout at zero bytes,
// and on EOF ends cleanly (socket removed, lock released — the store
// reopens).
func TestMCP_BecomesHolder(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	data := filepath.Join(dir, "data")
	c := startChildEnv(t, "--mcp", data, sock)
	c.waitSock(t, sock)

	// The store is open behind the socket: a real emit/recall works.
	cl := dial(t, sock)
	v, id, _, err := cl.Emit(opCtx(), rpcIn("takeover", "works", "yes"))
	if err != nil || v != rpc.VerdictInsert {
		t.Fatalf("emit through taken-over holder: v=%q id=%q err=%v", v, id, err)
	}
	recs, abst, err := cl.Recall(opCtx(), []string{"takeover"}, 5, "user")
	if err != nil || abst || len(recs) == 0 {
		t.Fatalf("recall through taken-over holder: %v abst=%v n=%d", err, abst, len(recs))
	}
	// Zero stdout bytes so far — the holder never speaks first.
	if c.stdout.Len() != 0 {
		t.Fatalf("MCP stdout must stay silent pre-call, got %q", c.stdout.String())
	}
	// EOF ends the holder cleanly; the socket file goes with it.
	_ = c.stdin.Close()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("clean EOF exit: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("child did not exit on stdin EOF")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket file must be removed on clean shutdown (no lock leak)")
	}
	// The lock is released: a fresh open of the same store works.
	s2, err := truth.Open(data)
	if err != nil {
		t.Fatalf("store reopen after EOF (lock leak?): %v", err)
	}
	_ = s2.Close()
}

// TestMCP_TwoHosts pins the race gate: first process holds, second
// connects over the socket — both work, one writer, no corruption
// (the second child never opens the store).
func TestMCP_TwoHosts(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	data := filepath.Join(dir, "data")
	first := startChildEnv(t, "--mcp", data, sock)
	first.waitSock(t, sock)
	// First host's data.
	cl := dial(t, sock)
	if _, _, _, err := cl.Emit(opCtx(), rpcIn("race", "host", "first")); err != nil {
		t.Fatalf("emit via first: %v", err)
	}
	// Second host: same env — must CONNECT, not take over.
	second := startChildEnv(t, "--mcp", data, sock)
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-second.done:
		t.Fatalf("second host must connect, not exit (lock fight = fail): %v", err)
	default:
	}
	// First host's data recalled THROUGH the second host's MCP
	// stdio — served from the first holder's store.
	f := second.mcpCall(t, 1, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if _, ok := f["result"]; !ok {
		t.Fatalf("handshake via second host: %v", f)
	}
	f = second.mcpCall(t, 2, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"terms":["race"],"caller_tier":"user","budget":5}}}`)
	res, _ := f["result"].(map[string]any)
	if res == nil {
		t.Fatalf("recall via second host: %v", f)
	}
	// The single writer remains the first process: it still serves.
	if _, _, err := cl.Recall(opCtx(), []string{"race"}, 5, "user"); err != nil {
		t.Fatalf("first host must still serve: %v", err)
	}
	_ = second.stdin.Close()
	_ = first.stdin.Close()
}

// TestMCP_KillRespawn pins the respawn gate: the holder dies mid-
// session (Process.Kill, abrupt), the next spawn recovers the store
// with the acked data — zero manual steps.
func TestMCP_KillRespawn(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	data := filepath.Join(dir, "data")
	c := startChildEnv(t, "--mcp", data, sock)
	c.waitSock(t, sock)
	cl := dial(t, sock)
	if _, _, _, err := cl.Emit(opCtx(), rpcIn("resurrect", "after", "kill")); err != nil {
		t.Fatalf("emit before kill: %v", err)
	}
	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}
	// Abrupt death, mid-session.
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-c.done
	// Respawn: same env, fresh process. Serve removes the stale
	// socket file; the store opens (lock released by the kill).
	next := startChildEnv(t, "--mcp", data, sock)
	next.waitSock(t, sock)
	cl2 := dial(t, sock)
	recs, abst, err := cl2.Recall(opCtx(), []string{"resurrect"}, 5, "user")
	if err != nil || abst || len(recs) == 0 {
		t.Fatalf("acked fact must survive the kill: %v abst=%v n=%d", err, abst, len(recs))
	}
	_ = next.stdin.Close()
}
