// Shim proof tests (Q1 exit: stdio vector, walk, fail-closed,
// stdout purity). The harness speaks raw JSON-RPC lines — no
// special-casing, the frames on the wire are the test.
package mcpshim

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	goClient "engram/client/go"
	"engram/internal/config"
	"engram/internal/daemon"
)

type rig struct {
	srv  *Server
	raw  *goClient.Client
	stop func()
}

// openRig boots a live daemon + dialed shim server (same discipline as
// the client walk: temp dirs, fast limiter, cleanup reverses opened).
func openRig(t *testing.T) *rig {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir() + "/data"
	cfg.RateLimitPerSec = 100000
	d, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock := t.TempDir() + "/engram.sock"
	go func() { _ = d.Serve(sock) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := goClient.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); d.Stop(); d.Close() })
	return &rig{srv: New(c), raw: c, stop: d.Stop}
}

// call runs one stdio session (lines in → lines out, synchronous to EOF)
// and returns the parsed response frames.
func (r *rig) call(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	in := strings.Join(lines, "\n") + "\n"
	var out bytes.Buffer
	if err := r.srv.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var frames []map[string]any
	for _, ln := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal([]byte(ln), &f); err != nil {
			t.Fatalf("stdout line is not JSON (purity broken): %q", ln)
		}
		if f["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC: %q", ln)
		}
		frames = append(frames, f)
	}
	return frames
}

func frame(id int, method, params string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"` + method + `","params":` + params + `}`
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

// TestShim_Handshake pins the lifecycle vector: initialize answers the
// version + tools capability, the initialized notification draws no
// reply, tools/list names the seven tools.
func TestShim_Handshake(t *testing.T) {
	r := openRig(t)
	frames := r.call(t,
		frame(1, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}`),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		frame(2, "tools/list", `{}`),
	)
	if len(frames) != 2 {
		t.Fatalf("want 2 replies (notification is silent), got %d", len(frames))
	}
	res, _ := frames[0]["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("handshake version: %v", res["protocolVersion"])
	}
	if _, ok := res["instructions"].(string); !ok {
		t.Fatal("handshake must carry instructions (hosts gate on it)")
	}
	res2, _ := frames[1]["result"].(map[string]any)
	tools, _ := res2["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("want 7 tools, got %d", len(tools))
	}
	want := map[string]bool{"remember": true, "recall": true, "correct": true, "attest": true, "resolve": true, "as_of": true, "consolidate": true}
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		delete(want, m["name"].(string))
		if _, ok := m["inputSchema"]; !ok {
			t.Fatalf("tool %v lacks inputSchema", m["name"])
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

// TestShim_ProtocolErrors pins the error split: malformed JSON,
// unknown method, and unknown tool are PROTOCOL errors (never
// isError results — those are reserved for core failures).
func TestShim_ProtocolErrors(t *testing.T) {
	r := openRig(t)
	frames := r.call(t,
		`not json at all`,
		frame(1, "nope/method", `{}`),
		frame(2, "tools/call", `{"name":"nope","arguments":{}}`),
	)
	if len(frames) != 3 {
		t.Fatalf("want 3 error frames, got %d", len(frames))
	}
	for i, want := range []float64{codeParseError, codeMethodAbsent, codeInvalidArgs} {
		e, _ := frames[i]["error"].(map[string]any)
		if e["code"] != want {
			t.Fatalf("frame %d: want code %v, got %v", i, want, e["code"])
		}
		if _, hasResult := frames[i]["result"]; hasResult {
			t.Fatalf("frame %d: protocol error must not carry result", i)
		}
	}
}

// TestShim_Walk drives remember→recall→attest over raw lines, then an
// abstaining recall — the ABSTAIN verdict must surface as honest text.
func TestShim_Walk(t *testing.T) {
	r := openRig(t)
	remember := frame(1, "tools/call", `{"name":"remember","arguments":{"subject":"pump","predicate":"runs","object":"yes","source":"s","session":"s1"}}`)
	frames := r.call(t, remember)
	text := resultText(t, frames[0])
	if !strings.Contains(text, "verdict=INSERT") || !strings.Contains(text, "id=") {
		t.Fatalf("remember: %q", text)
	}
	id := text[strings.Index(text, "id=")+3:]
	recall := frame(2, "tools/call", `{"name":"recall","arguments":{"terms":["pump"],"caller_tier":"user","budget":5}}`)
	frames = r.call(t, recall)
	text = resultText(t, frames[0])
	if !strings.Contains(text, "pump | runs | yes") || !strings.Contains(text, id) {
		t.Fatalf("recall: %q", text)
	}
	attest := frame(3, "tools/call", `{"name":"attest","arguments":{"id":"`+id+`","confirm":true,"source":"s","session":"s2"}}`)
	frames = r.call(t, attest)
	if text := resultText(t, frames[0]); !strings.Contains(text, "confidence=") {
		t.Fatalf("attest: %q", text)
	}
	miss := frame(4, "tools/call", `{"name":"recall","arguments":{"terms":["zzqq-unseen"],"caller_tier":"user"}}`)
	frames = r.call(t, miss)
	if text := resultText(t, frames[0]); !strings.Contains(text, "ABSTAIN") {
		t.Fatalf("miss must ABSTAIN, got %q", text)
	}
}

// TestShim_Consolidate pins the 7th verb (P2 operator trigger): a
// tick round-trips over raw lines and prints counts — stale ticks
// fail as isError tool results, never protocol errors.
func TestShim_Consolidate(t *testing.T) {
	r := openRig(t)
	frames := r.call(t,
		frame(1, "tools/call", `{"name":"consolidate","arguments":{"tick":0}}`),
	)
	text := resultText(t, frames[0])
	if !strings.Contains(text, "tick=0") || !strings.Contains(text, "consolidated=") {
		t.Fatalf("consolidate: %q", text)
	}
	frames = r.call(t,
		frame(2, "tools/call", `{"name":"consolidate","arguments":{"tick":0}}`),
	)
	res, _ := frames[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("re-tick must fail closed, got %v", frames[0])
	}
}

// TestShim_KillCore proves fail-closed: with the daemon stopped, calls
// return isError tool results — errors, never invented memories.
func TestShim_KillCore(t *testing.T) {
	r := openRig(t)
	r.stop()
	frames := r.call(t,
		frame(1, "tools/call", `{"name":"remember","arguments":{"subject":"x","predicate":"y","object":"z","source":"s","session":"s1"}}`),
		frame(2, "tools/call", `{"name":"recall","arguments":{"terms":["x"],"caller_tier":"user"}}`),
	)
	for i, f := range frames {
		res, _ := f["result"].(map[string]any)
		if res["isError"] != true {
			t.Fatalf("frame %d: dead core must be isError, got %v", i, f)
		}
	}
}

// TestShim_NilClient fails Serve fast: a shim with no core answers
// nothing, not even handshakes.
func TestShim_NilClient(t *testing.T) {
	if err := New(nil).Serve(strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("nil client must fail Serve fast")
	}
}

// TestShim_RawTextRejected pins the SRS 1.3 boundary by shape: remember
// takes structured fields — there is no raw-text argument to abuse.
func TestShim_RawTextRejected(t *testing.T) {
	r := openRig(t)
	frames := r.call(t,
		frame(1, "tools/call", `{"name":"remember","arguments":{"text":"the pump runs fine","source":"s","session":"s1"}}`),
	)
	res, _ := frames[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("raw-text remember must fail closed, got %v", frames[0])
	}
}

func resultText(t *testing.T, f map[string]any) string {
	t.Helper()
	res, _ := f["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected isError: %v", f)
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("empty content: %v", f)
	}
	m, _ := content[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}
